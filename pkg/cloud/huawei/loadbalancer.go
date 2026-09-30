package huawei

import (
	"sort"
	"strings"
	"sync"
	"time"

	bssintlmodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/bssintl/v2/model"

	"github.com/opencost/opencost/core/pkg/clustercache"
	"github.com/opencost/opencost/core/pkg/log"
	"github.com/opencost/opencost/pkg/cloud/models"
)

// elbIDAnnotation is the Service annotation Huawei CCE uses to bind a
// LoadBalancer Service to an ELB that already exists, typically one created
// outside the cluster (e.g. by Terraform). Confirmed against the CCE
// Services of a live stack.
const elbIDAnnotation = "kubernetes.io/elb.id"

const (
	// lbBillLookbackDays is how many complete days of billing history are
	// averaged into an ELB's hourly price. A week smooths the day-to-day swing
	// of elastic (LCU-billed) ELBs without lagging far behind a change.
	lbBillLookbackDays = 7

	// lbBillRefreshInterval bounds how often the billing history is re-read.
	// BSS publishes costs daily, so re-reading more often than a few times a
	// day only spends API quota.
	lbBillRefreshInterval = 6 * time.Hour

	// lbBillRetryInterval is how soon a failed read is retried.
	lbBillRetryInterval = 30 * time.Minute
)

// lbBillCache holds the hourly cost BSS billed for each ELB, keyed by the ELB
// ID as it appears in the elbIDAnnotation.
type lbBillCache struct {
	mu        sync.Mutex
	nextFetch time.Time
	hourly    map[string]float64
}

// ServiceLoadBalancerPricing prices the ELB behind a single Service.
//
// A Service bound to an existing ELB (elbIDAnnotation) is priced at what BSS
// actually billed for that ELB, averaged per hour over the last
// lbBillLookbackDays complete days. There is no list price to query instead:
// the ELBs these stacks create carry no fixed flavor, so Huawei Cloud bills
// them elastically by usage (LCU). The returned ProviderID is the ELB ID,
// which lets the cost model split the price between Services sharing the ELB
// and lets CostIntegration mark the ELB's billing rows as already counted by
// Kubernetes (see kubernetesresources.go), so the ELB is not counted twice.
//
// Until the ELB has a complete day of billing history (e.g. just after the
// stack is created), or when BSS cannot be reached, the flat LoadBalancerPricing
// rate stands in. Services without the annotation keep that flat rate too.
func (h *Huawei) ServiceLoadBalancerPricing(service *clustercache.Service) (*models.LoadBalancer, error) {
	elbID := strings.TrimSpace(service.Annotations[elbIDAnnotation])
	if elbID == "" {
		return h.LoadBalancerPricing()
	}

	if cost, ok := h.billedLoadBalancerHourlyCost(elbID); ok {
		return &models.LoadBalancer{Cost: cost, ProviderID: elbID}, nil
	}

	lb, err := h.LoadBalancerPricing()
	if err != nil {
		return nil, err
	}
	fallback := *lb
	fallback.ProviderID = elbID
	return &fallback, nil
}

// billedLoadBalancerHourlyCost returns the average hourly cost BSS billed for
// the ELB, refreshing the cache when it is due.
func (h *Huawei) billedLoadBalancerHourlyCost(elbID string) (float64, bool) {
	c := &h.lbBills
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now().UTC()
	if !now.Before(c.nextFetch) {
		hourly, err := fetchLoadBalancerHourlyCosts(now)
		if err != nil {
			log.Warnf("huawei cloud: reading ELB billing history failed, pricing Services bound to an ELB at the flat load balancer rate: %v", err)
			c.nextFetch = now.Add(lbBillRetryInterval)
		} else {
			c.hourly = hourly
			c.nextFetch = now.Add(lbBillRefreshInterval)
		}
	}

	cost, ok := c.hourly[elbID]
	return cost, ok
}

// fetchLoadBalancerHourlyCosts reads the last lbBillLookbackDays complete days
// of billing and returns the average hourly cost of every ELB in it, keyed by
// ELB ID.
func fetchLoadBalancerHourlyCosts(now time.Time) (map[string]float64, error) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	begin := today.AddDate(0, 0, -lbBillLookbackDays).Format(bssDateLayout)
	// BSS's end_time is inclusive: yesterday is the last complete day.
	end := today.AddDate(0, 0, -1).Format(bssDateLayout)

	// RESOURCE_ID alone is enough: the service code is packed into it.
	rows, err := fetchCostAnalysedBillsBy([]string{"RESOURCE_ID"}, begin, end, "ORIGINAL_COST", "NET_AMOUNT")
	if err != nil {
		return nil, err
	}
	return loadBalancerHourlyCosts(rows), nil
}

// loadBalancerHourlyCosts averages the daily ELB costs in rows into an hourly
// cost per ELB ID.
//
// Only the days an ELB was billed count towards its average, so an ELB
// created mid-window is not diluted by the days before it existed. Its first
// billed day is dropped when there are others, since that day is most likely
// partial and would drag the average down.
func loadBalancerHourlyCosts(rows []bssintlmodel.CostDataByDimension) map[string]float64 {
	daily := make(map[string]map[string]float64) // ELB ID -> day -> cost
	for _, row := range rows {
		elbID, ok := loadBalancerIDFromResourceID(dimensionValue(row.Dimensions, "RESOURCE_ID"))
		if !ok || row.Costs == nil {
			continue
		}
		for _, item := range *row.Costs {
			if item.TimeDimensionValue == nil || *item.TimeDimensionValue == "" {
				continue
			}
			amount, err := parseCostAmount(item.Amount)
			if err != nil {
				log.Warnf("huawei cloud: skipping unparsable ELB cost for %s: %v", elbID, err)
				continue
			}
			if daily[elbID] == nil {
				daily[elbID] = make(map[string]float64)
			}
			daily[elbID][*item.TimeDimensionValue] += amount
		}
	}

	hourly := make(map[string]float64, len(daily))
	for elbID, byDay := range daily {
		days := make([]string, 0, len(byDay))
		for day := range byDay {
			days = append(days, day)
		}
		sort.Strings(days)
		if len(days) > 1 {
			days = days[1:]
		}
		total := 0.0
		for _, day := range days {
			total += byDay[day]
		}
		hourly[elbID] = total / float64(len(days)*24)
	}
	return hourly
}

// loadBalancerIDFromResourceID returns the ELB ID a composite BSS RESOURCE_ID
// refers to, if it is an ELB.
func loadBalancerIDFromResourceID(resourceID string) (string, bool) {
	fields := strings.Split(resourceID, ":")
	if len(fields) != bssResourceIDFields || fields[0] != elbCloudServiceType {
		return "", false
	}
	id := fields[2]
	if id == "" || id == bssNullField {
		return "", false
	}
	return id, true
}

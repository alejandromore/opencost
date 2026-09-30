package huawei

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	bssintlmodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/bssintl/v2/model"

	"github.com/opencost/opencost/core/pkg/clustercache"
	"github.com/opencost/opencost/pkg/cloud/models"
	"github.com/opencost/opencost/pkg/env"
)

const (
	testELBID      = "0a1b2c3d-1111-2222-3333-444455556666"
	testOtherELBID = "9f8e7d6c-aaaa-bbbb-cccc-ddddeeeeffff"
)

func elbResourceID(id, name string) string {
	return elbCloudServiceType + ":hws.resource.type.elb:" + id + ":" + name
}

func costRow(resourceID string, dailyCosts map[string]string) bssintlmodel.CostDataByDimension {
	dims := []bssintlmodel.DimensionGroup{{Key: strPtr("RESOURCE_ID"), Value: strPtr(resourceID)}}
	costs := make([]bssintlmodel.Cost, 0, len(dailyCosts))
	for day, amount := range dailyCosts {
		costs = append(costs, bssintlmodel.Cost{TimeDimensionValue: strPtr(day), Amount: strPtr(amount)})
	}
	return bssintlmodel.CostDataByDimension{Dimensions: &dims, Costs: &costs}
}

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestLoadBalancerIDFromResourceID(t *testing.T) {
	cases := []struct {
		name, resourceID, wantID string
		wantOK                   bool
	}{
		{"elb", elbResourceID(testELBID, "elb-aiops-prod"), testELBID, true},
		{"elb without name", elbResourceID(testELBID, "null"), testELBID, true},
		{"not an elb", "hws.service.type.ec2:hws.resource.type.vm:" + testELBID + ":node-1", "", false},
		{"null id", elbResourceID("null", "elb"), "", false},
		{"not composite", testELBID, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := loadBalancerIDFromResourceID(tc.resourceID)
			if id != tc.wantID || ok != tc.wantOK {
				t.Fatalf("got (%q, %v), want (%q, %v)", id, ok, tc.wantID, tc.wantOK)
			}
		})
	}
}

func TestLoadBalancerHourlyCosts(t *testing.T) {
	rows := []bssintlmodel.CostDataByDimension{
		// Billed for three days; the first (partial) day is dropped.
		costRow(elbResourceID(testELBID, "elb-aiops-prod"), map[string]string{
			"2026-09-20": "1.00",
			"2026-09-21": "4.80",
			"2026-09-22": "2.40",
		}),
		// A second row for the same ELB (e.g. a separately billed LCU line)
		// adds to the same day.
		costRow(elbResourceID(testELBID, "elb-aiops-prod"), map[string]string{
			"2026-09-22": "2.40",
		}),
		// Billed for a single day: kept, nothing to drop.
		costRow(elbResourceID(testOtherELBID, "elb-sentinel"), map[string]string{
			"2026-09-22": "2.40",
		}),
		// Not an ELB: ignored.
		costRow("hws.service.type.ec2:hws.resource.type.vm:"+testELBID+":node-1", map[string]string{
			"2026-09-22": "100",
		}),
	}

	hourly := loadBalancerHourlyCosts(rows)

	// (4.80 + 4.80) / (2 days * 24h)
	if got := hourly[testELBID]; !approxEqual(got, 0.2) {
		t.Fatalf("expected 0.2/h for %s, got %v", testELBID, got)
	}
	if got := hourly[testOtherELBID]; !approxEqual(got, 0.1) {
		t.Fatalf("expected 0.1/h for %s, got %v", testOtherELBID, got)
	}
	if len(hourly) != 2 {
		t.Fatalf("expected only the two ELBs, got %v", hourly)
	}
}

func newLBTestProvider(t *testing.T) *Huawei {
	t.Helper()
	return &Huawei{
		Config:        &fakeProviderConfig{customPricing: &models.CustomPricing{DefaultLBPrice: "0.053"}},
		ClusterRegion: "la-south-2",
	}
}

func TestServiceLoadBalancerPricing_BilledELB(t *testing.T) {
	h := newLBTestProvider(t)
	h.lbBills.hourly = map[string]float64{testELBID: 0.2}
	h.lbBills.nextFetch = time.Now().Add(time.Hour)

	svc := &clustercache.Service{Annotations: map[string]string{elbIDAnnotation: testELBID}}
	lb, err := h.ServiceLoadBalancerPricing(svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lb.Cost != 0.2 || lb.ProviderID != testELBID {
		t.Fatalf("expected the billed price 0.2 for %s, got %+v", testELBID, lb)
	}
}

func TestServiceLoadBalancerPricing_UnbilledELBFallsBackToFlatRate(t *testing.T) {
	h := newLBTestProvider(t)
	h.lbBills.hourly = map[string]float64{}
	h.lbBills.nextFetch = time.Now().Add(time.Hour)

	svc := &clustercache.Service{Annotations: map[string]string{elbIDAnnotation: testELBID}}
	lb, err := h.ServiceLoadBalancerPricing(svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lb.Cost != 0.053 {
		t.Fatalf("expected the flat rate 0.053, got %v", lb.Cost)
	}
	// Still identified, so it is split between sharers and marked in the bill.
	if lb.ProviderID != testELBID {
		t.Fatalf("expected ProviderID %s, got %q", testELBID, lb.ProviderID)
	}
}

func TestServiceLoadBalancerPricing_NoAnnotationKeepsFlatRate(t *testing.T) {
	h := newLBTestProvider(t)

	lb, err := h.ServiceLoadBalancerPricing(&clustercache.Service{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lb.Cost != 0.053 || lb.ProviderID != "" {
		t.Fatalf("expected the flat rate with no ProviderID, got %+v", lb)
	}
}

// TestServiceLoadBalancerPricing_ReadsBSS drives the billing lookup against an
// httptest server standing in for BSS cost-analysed-bills.
func TestServiceLoadBalancerPricing_ReadsBSS(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"total_count": 1,
			"cost_data": [
				{
					"dimensions": [{"key": "RESOURCE_ID", "value": "` + elbResourceID(testELBID, "elb-aiops-prod") + `"}],
					"costs": [
						{"time_dimension_value": "2026-09-21", "amount": "2.40"},
						{"time_dimension_value": "2026-09-22", "amount": "2.40"}
					]
				}
			]
		}`))
	}))
	defer server.Close()

	bssEndpointOverride = server.URL
	defer func() { bssEndpointOverride = "" }()

	t.Setenv(env.HuaweiAccessKeyIDEnvVar, "test-ak")
	t.Setenv(env.HuaweiAccessKeySecretEnvVar, "test-sk")
	t.Setenv(env.HuaweiDomainIDEnvVar, "test-domain")

	h := newLBTestProvider(t)
	svc := &clustercache.Service{Annotations: map[string]string{elbIDAnnotation: testELBID}}

	for i := 0; i < 2; i++ {
		lb, err := h.ServiceLoadBalancerPricing(svc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !approxEqual(lb.Cost, 0.1) {
			t.Fatalf("expected 0.1/h from the bill, got %v", lb.Cost)
		}
	}
	if requests != 1 {
		t.Fatalf("expected the billing history to be read once and cached, got %d requests", requests)
	}
}

package costmodel

import (
	"math"
	"testing"

	v1 "k8s.io/api/core/v1"

	"github.com/opencost/opencost/core/pkg/clustercache"
	"github.com/opencost/opencost/pkg/cloud/models"
)

// flatLBProvider prices every load balancer at one flat rate, like most
// providers do.
type flatLBProvider struct {
	models.Provider
	cost float64
}

func (p *flatLBProvider) LoadBalancerPricing() (*models.LoadBalancer, error) {
	return &models.LoadBalancer{Cost: p.cost}, nil
}

// perServiceLBProvider prices the load balancer behind each Service from its
// "lb-id" annotation, like the Huawei provider does with kubernetes.io/elb.id.
type perServiceLBProvider struct {
	flatLBProvider
	prices map[string]float64
}

func (p *perServiceLBProvider) ServiceLoadBalancerPricing(svc *clustercache.Service) (*models.LoadBalancer, error) {
	id := svc.Annotations["lb-id"]
	if id == "" {
		return p.LoadBalancerPricing()
	}
	return &models.LoadBalancer{Cost: p.prices[id], ProviderID: id}, nil
}

func lbService(namespace, name, lbID string) *clustercache.Service {
	svc := &clustercache.Service{Namespace: namespace, Name: name, Type: v1.ServiceTypeLoadBalancer}
	if lbID != "" {
		svc.Annotations = map[string]string{"lb-id": lbID}
	}
	return svc
}

func lbCostsByService(t *testing.T, cm *CostModel) map[string]*models.LoadBalancer {
	t.Helper()
	lbs, err := cm.GetLBCost()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byService := make(map[string]*models.LoadBalancer, len(lbs))
	for key, lb := range lbs {
		byService[key.Namespace+"/"+key.Service] = lb
	}
	return byService
}

// TestGetLBCost_SharedLoadBalancerCountedOnce reproduces the AIOps stack, where
// the Istio gateway and the canary Services bind to the same ELB: the ELB's
// price must be split between them, not charged to each in full.
func TestGetLBCost_SharedLoadBalancerCountedOnce(t *testing.T) {
	cm := &CostModel{
		Cache: &clustercache.MockClusterCache{Services: []*clustercache.Service{
			lbService("istio-system", "istio-gateway", "elb-public"),
			lbService("aiops-canary", "canary", "elb-public"),
			lbService("istio-system", "istio-gateway-ingest", "elb-ingest"),
			lbService("default", "plain", ""),
			{Namespace: "default", Name: "cluster-ip", Type: v1.ServiceTypeClusterIP},
		}},
		Provider: &perServiceLBProvider{
			flatLBProvider: flatLBProvider{cost: 0.053},
			prices:         map[string]float64{"elb-public": 0.2, "elb-ingest": 0.3},
		},
	}

	got := lbCostsByService(t, cm)

	want := map[string]struct {
		cost       float64
		providerID string
	}{
		"istio-system/istio-gateway":        {0.1, "elb-public"},
		"aiops-canary/canary":               {0.1, "elb-public"},
		"istio-system/istio-gateway-ingest": {0.3, "elb-ingest"},
		"default/plain":                     {0.053, ""},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d load balancers, got %d: %v", len(want), len(got), got)
	}
	for svc, w := range want {
		lb, ok := got[svc]
		if !ok {
			t.Fatalf("missing load balancer for %s", svc)
		}
		if lb.Cost != w.cost || lb.ProviderID != w.providerID {
			t.Errorf("%s: got cost %v provider %q, want %v %q", svc, lb.Cost, lb.ProviderID, w.cost, w.providerID)
		}
	}
}

// TestGetLBCost_FlatRateProviderUnchanged checks that providers without
// per-Service pricing keep charging every LoadBalancer Service the flat rate.
func TestGetLBCost_FlatRateProviderUnchanged(t *testing.T) {
	cm := &CostModel{
		Cache: &clustercache.MockClusterCache{Services: []*clustercache.Service{
			lbService("a", "one", "same-lb"),
			lbService("b", "two", "same-lb"),
		}},
		Provider: &flatLBProvider{cost: 0.025},
	}

	for svc, lb := range lbCostsByService(t, cm) {
		if lb.Cost != 0.025 || lb.ProviderID != "" {
			t.Errorf("%s: got cost %v provider %q, want the flat 0.025 and no provider", svc, lb.Cost, lb.ProviderID)
		}
	}
}

func (p *perServiceLBProvider) IngressLoadBalancerPricing(ing *clustercache.Ingress) (*models.LoadBalancer, error) {
	id := ing.Annotations["lb-id"]
	if id == "" {
		return nil, nil
	}
	return &models.LoadBalancer{Cost: p.prices[id], ProviderID: id}, nil
}

// TestGetLBCost_IngressLoadBalancer reproduces Sentinel, where the ELB is bound
// to an Ingress rather than a Service: its price must reach the Ingress's
// backend Services, split with any Service bound to the same ELB, and be
// counted once in total.
func TestGetLBCost_IngressLoadBalancer(t *testing.T) {
	cm := &CostModel{
		Cache: &clustercache.MockClusterCache{
			Services: []*clustercache.Service{lbService("nucleo", "pii-gateway-lb", "elb-borde")},
			Ingresses: []*clustercache.Ingress{
				{Name: "borde", Namespace: "borde", Annotations: map[string]string{"lb-id": "elb-borde"},
					BackendServices: []string{"gateway", "frontend"}},
				{Name: "sin-elb", Namespace: "borde", BackendServices: []string{"otro"}},
			},
		},
		Provider: &perServiceLBProvider{
			flatLBProvider: flatLBProvider{cost: 0.053},
			prices:         map[string]float64{"elb-borde": 0.3},
		},
	}

	got := lbCostsByService(t, cm)
	want := []string{"nucleo/pii-gateway-lb", "borde/gateway", "borde/frontend"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	total := 0.0
	for _, svc := range want {
		lb, ok := got[svc]
		if !ok {
			t.Fatalf("missing %s in %v", svc, got)
		}
		if math.Abs(lb.Cost-0.1) > 1e-9 || lb.ProviderID != "elb-borde" {
			t.Errorf("%s: got cost %v provider %q, want 0.1 elb-borde", svc, lb.Cost, lb.ProviderID)
		}
		total += lb.Cost
	}
	if math.Abs(total-0.3) > 1e-9 {
		t.Fatalf("the ELB must be counted once in total: got %v, want 0.3", total)
	}
}

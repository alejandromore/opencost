package huawei

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/opencost/opencost/pkg/env"
)

const (
	testNodeECSID = "11111111-2222-3333-4444-555555555555"
	testPVEVSID   = "66666666-7777-8888-9999-000000000000"
)

type fakeKubernetesResources struct {
	ids []string
	err error
}

func (f *fakeKubernetesResources) KubernetesResourceIDs(start, end time.Time) ([]string, error) {
	return f.ids, f.err
}

func TestKubernetesResourceSet_Covers(t *testing.T) {
	set := newKubernetesResourceSet([]string{
		"cce://la-south-2/" + testNodeECSID, // a node providerID, decorated
		testPVEVSID,                         // a PV volume handle, bare
		testELBID,                           // an ELB from kubecost_load_balancer_cost
		"not-a-uuid",
	})

	cases := []struct {
		resourceID string
		want       bool
	}{
		{"hws.service.type.ec2:hws.resource.type.vm:" + testNodeECSID + ":node-1", true},
		{"hws.service.type.ebs:hws.resource.type.volume:" + testPVEVSID + ":pvc-abc", true},
		{elbResourceID(testELBID, "elb-aiops-prod"), true},
		// Case differences between the two sources do not matter.
		{elbResourceID("0A1B2C3D-1111-2222-3333-444455556666", "elb"), true},
		{elbResourceID(testOtherELBID, "elb-other"), false},
		{"hws.service.type.rds:hws.resource.type.rds.instance:57907bc4in01:rds", false},
		// A UUID that only appears in the free-form name is not the resource.
		{"hws.service.type.obs:hws.resource.type.obs:bucket:" + testNodeECSID, false},
	}
	for _, tc := range cases {
		if got := set.covers(tc.resourceID); got != tc.want {
			t.Errorf("covers(%q) = %v, want %v", tc.resourceID, got, tc.want)
		}
	}

	var empty kubernetesResourceSet
	if empty.covers(elbResourceID(testELBID, "elb")) {
		t.Fatalf("an empty set must cover nothing")
	}
}

func TestKubernetesResourcesIn(t *testing.T) {
	defer SetKubernetesResourceSource(nil)
	start := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)

	SetKubernetesResourceSource(nil)
	if set := kubernetesResourcesIn(start, end); set.covers(elbResourceID(testELBID, "elb")) {
		t.Fatalf("expected no coverage without a registered source")
	}

	SetKubernetesResourceSource(&fakeKubernetesResources{err: errors.New("prometheus down")})
	if set := kubernetesResourcesIn(start, end); set.covers(elbResourceID(testELBID, "elb")) {
		t.Fatalf("expected no coverage when the source fails")
	}

	SetKubernetesResourceSource(&fakeKubernetesResources{ids: []string{testELBID}})
	if set := kubernetesResourcesIn(start, end); !set.covers(elbResourceID(testELBID, "elb")) {
		t.Fatalf("expected the ELB to be covered")
	}
}

// TestCostIntegration_GetCloudCost_KubernetesPercent checks that the billing
// rows of resources Kubernetes allocation already covers -- here a Service's
// ELB and a node's ECS instance -- are marked KubernetesPercent 1, and the
// rest are left at 0, so allocation and the bill are never added up twice.
func TestCostIntegration_GetCloudCost_KubernetesPercent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"total_count": 3,
			"cost_data": [
				{
					"dimensions": [
						{"key": "RESOURCE_ID", "value": "` + elbResourceID(testELBID, "elb-aiops-prod") + `"},
						{"key": "CLOUD_SERVICE_TYPE", "value": "hws.service.type.elb"},
						{"key": "REGION_CODE", "value": "la-north-2"}
					],
					"costs": [{"time_dimension_value": "2026-09-22", "amount": "4.80", "official_amount": "4.80"}]
				},
				{
					"dimensions": [
						{"key": "RESOURCE_ID", "value": "hws.service.type.ec2:hws.resource.type.vm:` + testNodeECSID + `:node-1"},
						{"key": "CLOUD_SERVICE_TYPE", "value": "hws.service.type.ec2"},
						{"key": "REGION_CODE", "value": "la-north-2"}
					],
					"costs": [{"time_dimension_value": "2026-09-22", "amount": "10", "official_amount": "10"}]
				},
				{
					"dimensions": [
						{"key": "RESOURCE_ID", "value": "hws.service.type.rds:hws.resource.type.rds.instance:57907bc4in01:rds-dify"},
						{"key": "CLOUD_SERVICE_TYPE", "value": "hws.service.type.rds"},
						{"key": "REGION_CODE", "value": "la-north-2"}
					],
					"costs": [{"time_dimension_value": "2026-09-22", "amount": "20", "official_amount": "20"}]
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

	SetKubernetesResourceSource(&fakeKubernetesResources{ids: []string{
		testELBID,
		"cce://la-north-2/" + testNodeECSID,
	}})
	defer SetKubernetesResourceSource(nil)

	ci := &CostIntegration{CostConfiguration: CostConfiguration{ProjectID: "test-project", Region: "la-north-2"}}
	start := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	ccsr, err := ci.GetCloudCost(start, start.AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[string]float64{"elb": 1, "vm": 1, "rds.instance": 0}
	seen := 0
	for _, ccs := range ccsr.CloudCostSets {
		for _, cc := range ccs.CloudCosts {
			resourceType := cc.Properties.Labels[ResourceTypeLabel]
			pct, ok := want[resourceType]
			if !ok {
				t.Fatalf("unexpected resource type %q", resourceType)
			}
			seen++
			for name, metric := range map[string]float64{
				"list":          cc.ListCost.KubernetesPercent,
				"net":           cc.NetCost.KubernetesPercent,
				"amortized":     cc.AmortizedCost.KubernetesPercent,
				"amortized net": cc.AmortizedNetCost.KubernetesPercent,
				"invoiced":      cc.InvoicedCost.KubernetesPercent,
			} {
				if metric != pct {
					t.Errorf("%s: %s KubernetesPercent = %v, want %v", resourceType, name, metric, pct)
				}
			}
		}
	}
	if seen != 3 {
		t.Fatalf("expected 3 cloud costs, got %d", seen)
	}
}

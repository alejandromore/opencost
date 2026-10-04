package clustercache

import (
	"reflect"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func backend(name string) networkingv1.IngressBackend {
	return networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: name}}
}

func TestTransformIngress_BackendServices(t *testing.T) {
	def := backend("frontend")
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "borde", Namespace: "sentinel-borde",
			Annotations: map[string]string{"kubernetes.io/elb.id": "elb-1"}},
		Spec: networkingv1.IngressSpec{
			DefaultBackend: &def,
			Rules: []networkingv1.IngressRule{
				{IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{{Backend: backend("gateway")}, {Backend: backend("frontend")}},
				}}},
				{}, // a rule without HTTP paths
			},
		},
	}
	got := TransformIngress(ing)
	if !reflect.DeepEqual(got.BackendServices, []string{"frontend", "gateway"}) {
		t.Fatalf("expected unique backends [frontend gateway], got %v", got.BackendServices)
	}
	if got.Annotations["kubernetes.io/elb.id"] != "elb-1" || got.Namespace != "sentinel-borde" {
		t.Fatalf("expected annotations and namespace kept, got %+v", got)
	}
}

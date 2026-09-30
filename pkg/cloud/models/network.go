package models

import "github.com/opencost/opencost/core/pkg/clustercache"

// TODO: used for dynamic cloud provider price fetching.
// determine what identifies a load balancer in the json returned from the cloud provider pricing API call
// type LBKey interface {
// }

// Network is the interface by which the provider and cost model communicate network egress prices.
// The provider will best-effort try to fill out this struct.
type Network struct {
	ZoneNetworkEgressCost     float64
	RegionNetworkEgressCost   float64
	InternetNetworkEgressCost float64
	NatGatewayEgressCost      float64
	NatGatewayIngressCost     float64
}

// LoadBalancer is the interface by which the provider and cost model communicate LoadBalancer prices.
// The provider will best-effort try to fill out this struct.
type LoadBalancer struct {
	IngressIPAddresses []string `json:"IngressIPAddresses"`
	Cost               float64  `json:"hourlyCost"`
	// ProviderID identifies the cloud load balancer backing a Service, when the
	// provider can tell which one it is. Several Services may bind to the same
	// load balancer; the cost model splits its Cost evenly between them so the
	// load balancer is counted once.
	ProviderID string `json:"providerID,omitempty"`
}

// ServiceLoadBalancerPricer is implemented by providers that can price the
// specific load balancer behind a Service, instead of the single flat rate
// LoadBalancerPricing returns for every Service. The cost model uses it in
// preference to LoadBalancerPricing when a provider implements it.
type ServiceLoadBalancerPricer interface {
	ServiceLoadBalancerPricing(service *clustercache.Service) (*LoadBalancer, error)
}

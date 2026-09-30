package huawei

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/opencost/opencost/core/pkg/log"
)

// KubernetesResourceSource reports the IDs of the billed resources that back
// Kubernetes objects OpenCost already allocates: the ECS instances of nodes,
// the EVS volumes of persistent volumes and the ELBs of LoadBalancer Services.
//
// CostIntegration uses it to set KubernetesPercent on those resources'
// billing rows. Without that, a report that adds allocation (Kubernetes) to
// cloud costs (the bill) counts every node, volume and load balancer twice.
// The cost model registers an implementation backed by its metrics data
// source (see SetKubernetesResourceSource); none is registered when OpenCost
// runs without one, and KubernetesPercent then stays 0 as before.
type KubernetesResourceSource interface {
	// KubernetesResourceIDs returns the provider IDs seen in [start, end).
	// The IDs may be decorated (e.g. "cce://.../<uuid>"); only the UUIDs in
	// them are used for matching.
	KubernetesResourceIDs(start, end time.Time) ([]string, error)
}

var (
	kubernetesResourcesMu sync.RWMutex
	kubernetesResources   KubernetesResourceSource
)

// SetKubernetesResourceSource registers the source CostIntegration uses to tell
// which billed resources Kubernetes allocation already covers.
func SetKubernetesResourceSource(source KubernetesResourceSource) {
	kubernetesResourcesMu.Lock()
	defer kubernetesResourcesMu.Unlock()
	kubernetesResources = source
}

func getKubernetesResourceSource() KubernetesResourceSource {
	kubernetesResourcesMu.RLock()
	defer kubernetesResourcesMu.RUnlock()
	return kubernetesResources
}

// kubernetesResourcesIn returns the resources Kubernetes allocation covers in
// [start, end), or an empty set when no source is registered or it fails: the
// bill is still reported, just without KubernetesPercent.
func kubernetesResourcesIn(start, end time.Time) kubernetesResourceSet {
	source := getKubernetesResourceSource()
	if source == nil {
		return nil
	}
	providerIDs, err := source.KubernetesResourceIDs(start, end)
	if err != nil {
		log.Warnf("huawei cloud cost: cannot tell which billed resources Kubernetes allocation covers, reporting KubernetesPercent 0: %v", err)
		return nil
	}
	return newKubernetesResourceSet(providerIDs)
}

// uuidPattern matches the UUIDs Huawei Cloud uses as resource IDs (ECS
// instances, EVS volumes, ELBs).
var uuidPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// kubernetesResourceSet is the set of resource UUIDs Kubernetes allocation
// covers. Matching on UUIDs rather than whole strings is what lets a node's
// providerID ("cce://<cluster>/<uuid>" or similar) meet the ECS instance ID in
// a BSS RESOURCE_ID ("hws.service.type.ec2:hws.resource.type.vm:<uuid>:<name>").
type kubernetesResourceSet map[string]struct{}

func newKubernetesResourceSet(providerIDs []string) kubernetesResourceSet {
	set := make(kubernetesResourceSet)
	for _, providerID := range providerIDs {
		for _, id := range uuidPattern.FindAllString(providerID, -1) {
			set[strings.ToLower(id)] = struct{}{}
		}
	}
	return set
}

// covers reports whether the billed resource is one Kubernetes allocation
// already accounts for. Only the resource ID field of a composite RESOURCE_ID
// is checked: the other fields are codes and a free-form name.
func (s kubernetesResourceSet) covers(resourceID string) bool {
	if len(s) == 0 {
		return false
	}
	id := resourceID
	if fields := strings.Split(resourceID, ":"); len(fields) == bssResourceIDFields {
		id = fields[2]
	}
	for _, uuid := range uuidPattern.FindAllString(id, -1) {
		if _, ok := s[strings.ToLower(uuid)]; ok {
			return true
		}
	}
	return false
}

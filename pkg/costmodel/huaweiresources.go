package costmodel

import (
	"fmt"
	"time"

	"github.com/opencost/opencost/core/pkg/util/timeutil"
	"github.com/opencost/opencost/modules/prometheus-source/pkg/prom"
	"github.com/opencost/opencost/pkg/cloud/huawei"
)

// huaweiKubernetesResourcesQuery lists the provider IDs of every node,
// persistent volume and load balancer the cost model priced in a window --
// the resources whose billing rows Huawei CostIntegration must mark as already
// counted by Kubernetes allocation. It is evaluated at the window end, over
// the window's duration, and spans every cluster in the data source, which is
// what a hub collecting several clusters needs.
const huaweiKubernetesResourcesQuery = `group by (provider_id) (last_over_time(node_total_hourly_cost[%[1]s]))
or group by (provider_id) (last_over_time(pv_hourly_cost[%[1]s]))
or group by (provider_id) (last_over_time(kubecost_load_balancer_cost{provider_id!=""}[%[1]s]))`

// promHuaweiKubernetesResources implements huawei.KubernetesResourceSource on
// the Prometheus data source the cost model already queries.
type promHuaweiKubernetesResources struct {
	contexts *prom.ContextFactory
}

func (p *promHuaweiKubernetesResources) KubernetesResourceIDs(start, end time.Time) ([]string, error) {
	dur := end.Sub(start)
	if dur <= 0 {
		return nil, fmt.Errorf("empty window [%s, %s)", start, end)
	}
	query := fmt.Sprintf(huaweiKubernetesResourcesQuery, timeutil.DurationString(dur))

	results, err := p.contexts.NewNamedContext("huawei-kubernetes-resources").QueryAtTime(query, end).Await()
	if err != nil {
		return nil, fmt.Errorf("querying Kubernetes provider IDs: %w", err)
	}

	ids := make([]string, 0, len(results))
	for _, result := range results {
		if id, err := result.GetProviderID(); err == nil && id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// registerHuaweiKubernetesResources lets Huawei cloud costs tell which billed
// resources Kubernetes allocation covers. Only a Prometheus data source can
// answer that over an arbitrary window; with any other the bill is reported
// without KubernetesPercent, as before.
func registerHuaweiKubernetesResources(pds *prom.PrometheusDataSource) {
	huawei.SetKubernetesResourceSource(&promHuaweiKubernetesResources{contexts: pds.PrometheusContexts()})
}

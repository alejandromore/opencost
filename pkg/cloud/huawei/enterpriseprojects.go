package huawei

import (
	"strings"
)

// Label keys for the Enterprise Project a billed resource belongs to.
const (
	EnterpriseProjectIDLabel = "huawei_enterprise_project_id"
	EnterpriseProjectLabel   = "huawei_enterprise_project"
	// PlatformLabel carries the platform the resource's Enterprise Project
	// holds, as configured in CostConfiguration.EnterpriseProjects.
	PlatformLabel = "platform"
)

// defaultEnterpriseProjectID is the ID BSS reports for the "default"
// Enterprise Project. Confirmed against a real Huawei Cloud bill export, where
// MaaS token usage is billed to it.
const defaultEnterpriseProjectID = "0"

// enterpriseProjectDimension is the cost-analysed-bills group-by key for a
// resource's Enterprise Project. It follows the naming of the other dimension
// keys this integration uses (RESOURCE_ID, REGION_CODE).
const enterpriseProjectDimension = "ENTERPRISE_PROJECT_ID"

// fetchResourceEnterpriseProjects maps every resource billed in [beginTime,
// endTime] to its Enterprise Project ID.
//
// It is a query of its own because BSS caps a cost query at three group-by
// dimensions and the main query (costQueryDimensions) already uses them. A
// resource belongs to one Enterprise Project at a time, so grouping by just
// resource and Enterprise Project is enough to label every row of the main
// query.
func fetchResourceEnterpriseProjects(beginTime, endTime string) (map[string]string, error) {
	rows, err := fetchCostAnalysedBillsBy([]string{"RESOURCE_ID", enterpriseProjectDimension}, beginTime, endTime, "ORIGINAL_COST", "NET_AMOUNT")
	if err != nil {
		return nil, err
	}
	projects := make(map[string]string, len(rows))
	for _, row := range rows {
		resourceID := dimensionValue(row.Dimensions, "RESOURCE_ID")
		epID := strings.TrimSpace(dimensionValue(row.Dimensions, enterpriseProjectDimension))
		if resourceID == "" || epID == "" || epID == bssNullField {
			continue
		}
		projects[describeResource(resourceID).ID] = epID
	}
	return projects, nil
}

// serviceCodeOf returns the service type code packed into a composite BSS
// RESOURCE_ID, or "" when the ID is not in that form.
func serviceCodeOf(resourceID string) string {
	fields := strings.Split(resourceID, ":")
	if len(fields) != bssResourceIDFields {
		return ""
	}
	return fields[0]
}

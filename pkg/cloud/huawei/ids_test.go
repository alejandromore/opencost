package huawei

import (
	"testing"

	v1 "k8s.io/api/core/v1"

	"github.com/opencost/opencost/core/pkg/clustercache"
)

// TestNodeProviderID checks a CCE node is reported under its ECS instance ID
// (the system UUID), not the CCE node ID CCE puts in spec.providerID: the
// bill identifies the machine by the former.
func TestNodeProviderID(t *testing.T) {
	h := &Huawei{}
	node := &clustercache.Node{
		SpecProviderID: "f98fb419-bc61-11f1-b9c4-0255ac10023e",
		Status:         v1.NodeStatus{NodeInfo: v1.NodeSystemInfo{SystemUUID: "FF018B03-AB42-4F50-A76D-984667EEBB12"}},
	}
	if got := h.NodeProviderID(node); got != "ff018b03-ab42-4f50-a76d-984667eebb12" {
		t.Fatalf("expected the ECS instance ID, got %q", got)
	}
	if got := h.NodeProviderID(&clustercache.Node{SpecProviderID: "x"}); got != "" {
		t.Fatalf("expected empty without a system UUID (spec.providerID is kept), got %q", got)
	}
}

// TestGetPVKey_EVSVolumeID checks an everest PV carries its EVS volume ID.
func TestGetPVKey_EVSVolumeID(t *testing.T) {
	h := &Huawei{}
	pv := &clustercache.PersistentVolume{Spec: v1.PersistentVolumeSpec{
		StorageClassName: "csi-disk",
		PersistentVolumeSource: v1.PersistentVolumeSource{
			CSI: &v1.CSIPersistentVolumeSource{Driver: "disk.csi.everest.io", VolumeHandle: testPVEVSID},
		},
	}}
	if got := h.GetPVKey(pv, nil, "la-south-2").ID(); got != testPVEVSID {
		t.Fatalf("expected the EVS volume ID %s, got %q", testPVEVSID, got)
	}
	if got := h.GetPVKey(&clustercache.PersistentVolume{}, nil, "la-south-2").ID(); got != "" {
		t.Fatalf("expected no ID for a non-CSI volume, got %q", got)
	}
}

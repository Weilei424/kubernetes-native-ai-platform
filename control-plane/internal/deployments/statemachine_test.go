// control-plane/internal/deployments/statemachine_test.go
package deployments_test

import (
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
)

func TestValidTransition_PendingToProvisioning(t *testing.T) {
	if !deployments.ValidTransition("pending", "provisioning") {
		t.Fatal("pending → provisioning must be valid")
	}
}

func TestValidTransition_ProvisioningToRunning(t *testing.T) {
	if !deployments.ValidTransition("provisioning", "running") {
		t.Fatal("provisioning → running must be valid")
	}
}

func TestValidTransition_ProvisioningToFailed(t *testing.T) {
	if !deployments.ValidTransition("provisioning", "failed") {
		t.Fatal("provisioning → failed must be valid")
	}
}

func TestValidTransition_RunningToFailed(t *testing.T) {
	if !deployments.ValidTransition("running", "failed") {
		t.Fatal("running → failed must be valid")
	}
}

func TestValidTransition_AnyToDeleting(t *testing.T) {
	for _, from := range []string{"pending", "provisioning", "running", "failed"} {
		if !deployments.ValidTransition(from, "deleting") {
			t.Fatalf("%s → deleting must be valid", from)
		}
	}
}

func TestValidTransition_DeletingToDeleted(t *testing.T) {
	if !deployments.ValidTransition("deleting", "deleted") {
		t.Fatal("deleting → deleted must be valid")
	}
}

func TestValidTransition_Invalid(t *testing.T) {
	if deployments.ValidTransition("running", "pending") {
		t.Fatal("running → pending must be invalid")
	}
	if deployments.ValidTransition("running", "provisioning") {
		t.Fatal("running → provisioning must be invalid")
	}
	if deployments.ValidTransition("failed", "running") {
		t.Fatal("failed → running must be invalid")
	}
	if deployments.ValidTransition("deleted", "running") {
		t.Fatal("deleted → running must be invalid")
	}
	// Direct any→deleted is no longer valid; must go through deleting.
	if deployments.ValidTransition("running", "deleted") {
		t.Fatal("running → deleted must be invalid (must go through deleting)")
	}
	if deployments.ValidTransition("pending", "deleted") {
		t.Fatal("pending → deleted must be invalid (must go through deleting)")
	}
}

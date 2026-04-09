// control-plane/internal/deployments/statemachine.go
package deployments

// validTransitions defines the allowed from → to pairs for deployment status.
//
// Deletion is a two-step process: user-initiated DELETE sets status to "deleting"
// so the operator can clean up Kubernetes resources; the operator then transitions
// to "deleted" once the pod and service are removed.
var validTransitions = map[string]map[string]bool{
	"pending": {
		"provisioning": true,
		"deleting":     true,
	},
	"provisioning": {
		"running":  true,
		"failed":   true,
		"deleting": true,
	},
	"running": {
		"provisioning": true, // pod was lost (eviction, node failure); reconciler recreates and re-provisions
		"failed":       true,
		"deleting":     true,
	},
	"failed": {
		"deleting": true,
	},
	"deleting": {
		"deleted": true,
	},
}

// ValidTransition reports whether transitioning from → to is a legal deployment
// status change.
func ValidTransition(from, to string) bool {
	targets, ok := validTransitions[from]
	if !ok {
		return false
	}
	return targets[to]
}

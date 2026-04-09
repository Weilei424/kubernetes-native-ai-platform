// control-plane/internal/deployments/statemachine.go
package deployments

// validTransitions defines the allowed from → to pairs for deployment status.
var validTransitions = map[string]map[string]bool{
	"pending": {
		"provisioning": true,
		"deleted":      true,
	},
	"provisioning": {
		"running": true,
		"failed":  true,
		"deleted": true,
	},
	"running": {
		"failed":  true,
		"deleted": true,
	},
	"failed": {
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

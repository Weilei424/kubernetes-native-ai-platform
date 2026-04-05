// control-plane/internal/jobs/statemachine.go
package jobs

import "fmt"

var validTransitions = map[string]map[string]bool{
	"PENDING": {"QUEUED": true, "CANCELLED": true},
	"QUEUED":  {"RUNNING": true, "FAILED": true, "CANCELLED": true},
	"RUNNING": {"SUCCEEDED": true, "FAILED": true, "CANCELLED": true},
}

// ValidateTransition returns nil if transitioning from → to is allowed,
// or an error describing the invalid transition.
func ValidateTransition(from, to string) error {
	allowed, ok := validTransitions[from]
	if !ok {
		return fmt.Errorf("unknown status %q", from)
	}
	if !allowed[to] {
		return fmt.Errorf("invalid transition %s → %s", from, to)
	}
	return nil
}

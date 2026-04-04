// control-plane/internal/auth/store.go
package auth

import "context"

// TokenStore looks up a plaintext bearer token and returns the owning tenant ID.
// Returns an error if the token is not found or is expired.
type TokenStore interface {
	FindToken(ctx context.Context, plaintext string) (tenantID string, err error)
}

// control-plane/internal/auth/middleware.go
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type contextKey string

const tenantIDKey contextKey = "tenant_id"

// TokenAuth returns a middleware that validates the Bearer token in the
// Authorization header using the provided TokenStore. On success it injects
// the tenant ID into the request context. On failure it returns 401.
func TokenAuth(store TokenStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearer(r)
			if token == "" {
				writeUnauthorized(w, "missing or malformed Authorization header")
				return
			}

			tenantID, err := store.FindToken(r.Context(), token)
			if err != nil {
				writeUnauthorized(w, "invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), tenantIDKey, tenantID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// TenantIDFromContext extracts the tenant ID injected by TokenAuth.
// Returns "" if not present.
func TenantIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(tenantIDKey).(string)
	return v
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(h, "Bearer ")
}

func writeUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}

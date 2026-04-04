// control-plane/internal/auth/postgres_store.go
package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// PostgresTokenStore implements TokenStore against the api_tokens table.
type PostgresTokenStore struct {
	db *pgxpool.Pool
}

// NewPostgresTokenStore creates a PostgresTokenStore backed by the given pool.
func NewPostgresTokenStore(db *pgxpool.Pool) *PostgresTokenStore {
	return &PostgresTokenStore{db: db}
}

// FindToken scans api_tokens for a row whose token_hash matches the plaintext
// via bcrypt. Returns the tenant_id of the first matching, non-expired token.
func (s *PostgresTokenStore) FindToken(ctx context.Context, plaintext string) (string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT tenant_id::text, token_hash, expires_at FROM api_tokens`)
	if err != nil {
		return "", fmt.Errorf("query api_tokens: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var tenantID, hash string
		var expiresAt *time.Time
		if err := rows.Scan(&tenantID, &hash, &expiresAt); err != nil {
			continue
		}
		if expiresAt != nil && time.Now().After(*expiresAt) {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil {
			return tenantID, nil
		}
	}
	return "", fmt.Errorf("token not found")
}

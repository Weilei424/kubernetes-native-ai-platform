// control-plane/internal/auth/postgres_store_test.go
package auth_test

import (
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/auth"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

func TestFindToken_PrefixLookup(t *testing.T) {
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	// Insert a tenant
	var tenantID string
	err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name) VALUES ('test-tenant') RETURNING id::text`,
	).Scan(&tenantID)
	if err != nil {
		t.Fatalf("insert tenant: %v", err)
	}

	// Issue a plaintext token and store hash + prefix
	plaintext := "abcdefgh-1234-5678-rest-of-token"
	prefix := plaintext[:8] // "abcdefgh"
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}

	_, err = pool.Exec(ctx,
		`INSERT INTO api_tokens (tenant_id, token_hash, token_prefix) VALUES ($1, $2, $3)`,
		tenantID, string(hash), prefix,
	)
	if err != nil {
		t.Fatalf("insert token: %v", err)
	}

	store := auth.NewPostgresTokenStore(pool)

	// Valid token resolves to tenant
	got, err := store.FindToken(ctx, plaintext)
	if err != nil {
		t.Fatalf("FindToken: %v", err)
	}
	if got != tenantID {
		t.Fatalf("expected tenantID %q, got %q", tenantID, got)
	}

	// Wrong token returns error
	_, err = store.FindToken(ctx, "wrongtoken-that-has-no-prefix")
	if err == nil {
		t.Fatal("expected error for wrong token, got nil")
	}
}

func TestFindToken_ExpiredToken(t *testing.T) {
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID string
	err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name) VALUES ('expired-tenant') RETURNING id::text`,
	).Scan(&tenantID)
	if err != nil {
		t.Fatalf("insert tenant: %v", err)
	}

	plaintext := "exptoken-xxxx-yyyy-zzzz"
	prefix := plaintext[:8]
	hash, _ := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	past := time.Now().Add(-time.Hour)

	_, err = pool.Exec(ctx,
		`INSERT INTO api_tokens (tenant_id, token_hash, token_prefix, expires_at) VALUES ($1, $2, $3, $4)`,
		tenantID, string(hash), prefix, past,
	)
	if err != nil {
		t.Fatalf("insert expired token: %v", err)
	}

	store := auth.NewPostgresTokenStore(pool)
	_, err = store.FindToken(ctx, plaintext)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}

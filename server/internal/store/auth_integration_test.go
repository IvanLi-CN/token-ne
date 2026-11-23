package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/google/uuid"
)

func TestAuthStoreIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo, e := Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer repo.Close()
	for _, statement := range []string{
		"delete from auth_flows",
		"delete from admin_oidc_identities",
		"delete from auth_settings",
		"delete from audit_events",
		"delete from admin_users",
	} {
		if _, e = repo.Pool.Exec(ctx, statement); e != nil {
			t.Fatalf("clean auth state with %q: %v", statement, e)
		}
	}
	if e = repo.Bootstrap(ctx, "admin", "password"); e != nil {
		t.Fatal(e)
	}
	admin, e := repo.AdminByUsername(ctx, "admin")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.AuthSettings(ctx); !errors.Is(e, ErrNotFound) {
		t.Fatalf("empty settings: %v", e)
	}
	ciphertext, nonce, e := auth.Encrypt(make([]byte, 32), "secret")
	if e != nil {
		t.Fatal(e)
	}
	issuer, clientID := "https://id.example", "token-ne"
	row, e := repo.SaveAuthSettings(ctx, AuthSettings{
		OIDCEnabled: true, IssuerURL: &issuer, ClientID: &clientID,
		ClientSecretCiphertext: ciphertext, ClientSecretNonce: nonce,
		Scopes: []string{"openid"}, LocalLoginEnabled: true, SessionTTLHours: 720,
	}, 0, false, admin.ID, Audit{Action: "test"})
	if e != nil || row.Revision != 1 {
		t.Fatalf("save settings: %+v %v", row, e)
	}
	got, e := repo.AuthSettings(ctx)
	if e != nil || got.ClientSecretCiphertext == nil || got.Scopes[0] != "openid" {
		t.Fatalf("load settings: %+v %v", got, e)
	}
	admin, e = repo.AdminByID(ctx, admin.ID)
	if e != nil {
		t.Fatal(e)
	}
	version, e := repo.ReplaceOIDCBinding(ctx, admin.ID, issuer, "subject", Audit{Action: "bind"})
	if e != nil || version != admin.SessionVersion+1 {
		t.Fatalf("binding version: %d %v", version, e)
	}
	binding, e := repo.OIDCBinding(ctx, admin.ID)
	if e != nil || binding.Issuer != issuer || binding.Subject != "subject" {
		t.Fatalf("binding: %+v %v", binding, e)
	}
	state := sha256.Sum256([]byte("state"))
	flow := AuthFlow{ID: uuid.NewString(), StateHash: state[:], Nonce: "nonce", PKCEVerifierCiphertext: []byte("c"), PKCEVerifierNonce: []byte("n"), Mode: "login", ExpiresAt: time.Now().Add(time.Minute)}
	if e = repo.CreateAuthFlow(ctx, flow); e != nil {
		t.Fatal(e)
	}
	loaded, e := repo.AuthFlow(ctx, state[:])
	if e != nil || loaded.ID != flow.ID {
		t.Fatalf("flow: %+v %v", loaded, e)
	}
	if e = repo.ConsumeAuthFlow(ctx, flow.ID, time.Now()); e != nil {
		t.Fatal(e)
	}
	if e = repo.ConsumeAuthFlow(ctx, flow.ID, time.Now()); !errors.Is(e, ErrConflict) {
		t.Fatalf("flow replay: %v", e)
	}
}

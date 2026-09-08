// Package testutil creates isolated synthetic SOPS fixtures, never user keys.
package testutil

import (
	"context"
	"path/filepath"
	"testing"

	"tapas/internal/vault"
)

func Vault(t *testing.T) *vault.Store {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "test-identity.txt")
	recipient, e := vault.GenerateIdentity(key)
	if e != nil {
		t.Fatal(e)
	}
	s, e := vault.New(filepath.Join(dir, "vault.sops.json"), key)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Init(context.Background(), "test", recipient); e != nil {
		t.Fatal(e)
	}
	return s
}

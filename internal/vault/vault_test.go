package vault_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tapas/internal/testutil"
	"tapas/internal/vault"
)

func metadata() vault.Metadata {
	return vault.Metadata{Name: "Fixture", Service: "fixture", Environment: "development", SuggestedEnv: "API_TOKEN"}
}
func TestRoundTripReplacementAndDiscovery(t *testing.T) {
	s := testutil.Vault(t)
	ctx := context.Background()
	snap, e := s.Discover()
	if e != nil {
		t.Fatal(e)
	}
	secret := "synthetic-" + vault.Token()
	ref, e := s.Save(ctx, snap.Revision, metadata(), secret, "", false)
	if e != nil {
		t.Fatal(e)
	}
	after, e := s.Discover()
	if e != nil || len(after.Credentials) != 1 {
		t.Fatal("missing metadata", e)
	}
	if !strings.HasSuffix(ref, "/"+after.Credentials[0].ID) {
		t.Fatal("wrong saved reference")
	}
	b, _ := os.ReadFile(s.Path)
	if bytes.Contains(b, []byte(secret)) {
		t.Fatal("plaintext on disk")
	}
	if _, e = s.Save(ctx, snap.Revision, metadata(), secret, "", false); !errors.Is(e, vault.ErrConflict) {
		t.Fatal("stale write accepted")
	}
	id := after.Credentials[0].ID
	m := metadata()
	m.Name = "Renamed"
	if _, e = s.Save(ctx, after.Revision, m, "replacement", id, false); e == nil {
		t.Fatal("unconfirmed replacement accepted")
	}
	replaced, e := s.Save(ctx, after.Revision, m, "replacement", id, true)
	if e != nil || replaced != ref {
		t.Fatal("replacement changed ID", e)
	}
	now, _ := s.Discover()
	if now.Credentials[0].Name != "Renamed" {
		t.Fatal("rename not saved")
	}
	// Discovery must not depend on SOPS execution or identity availability.
	s.Binary = "/missing-sops"
	if _, e = s.Discover(); e != nil {
		t.Fatal("discovery tried to decrypt")
	}
	before, _ := os.ReadFile(s.Path)
	if _, e = s.Save(ctx, now.Revision, m, "new", id, true); !errors.Is(e, vault.ErrDecrypt) {
		t.Fatal("wrong setup failure")
	}
	unchanged, _ := os.ReadFile(s.Path)
	if !bytes.Equal(before, unchanged) {
		t.Fatal("failed save changed store")
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(s.Path), ".tapas-encrypted-*"))
	if len(files) != 0 {
		t.Fatal("staging artifacts retained")
	}
}
func TestConcurrentSave(t *testing.T) {
	s := testutil.Vault(t)
	snap, _ := s.Discover()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			m := metadata()
			m.Name = name
			_, e := s.Save(context.Background(), snap.Revision, m, "synthetic", "", false)
			results <- e
		}(name)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, vault.ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent updates not serialized")
	}
}
func TestSchemaAndInitProtection(t *testing.T) {
	s := testutil.Vault(t)
	before, _ := os.ReadFile(s.Path)
	if e := s.Init(context.Background(), "test", "age1invalid"); e == nil {
		t.Fatal("existing store overwritten")
	}
	after, _ := os.ReadFile(s.Path)
	if !bytes.Equal(before, after) {
		t.Fatal("init changed existing store")
	}
	malformed := bytes.Replace(before, []byte(`"schema": 1`), []byte(`"schema": 1, "schema": 1`), 1)
	if bytes.Equal(malformed, before) {
		t.Fatal("fixture substitution failed")
	}
	os.WriteFile(s.Path, malformed, 0600)
	if _, e := s.Discover(); e == nil {
		t.Fatal("duplicate keys accepted")
	}
	for _, n := range []string{"PATH", "BASH_ENV", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "NODE_OPTIONS", "PYTHONPATH", "SOPS_AGE_KEY", "bad-name"} {
		m := metadata()
		m.SuggestedEnv = n
		if m.Validate() == nil {
			t.Fatalf("accepted control variable %s", n)
		}
	}
}

func TestManagedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "identity.txt")
	recipient, e := vault.GenerateIdentity(path)
	if e != nil || !strings.HasPrefix(recipient, "age1") {
		t.Fatal("identity generation failed", e)
	}
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("identity permissions are not private", e)
	}
	if _, e = vault.GenerateIdentity(path); e == nil {
		t.Fatal("existing identity overwritten")
	}
}

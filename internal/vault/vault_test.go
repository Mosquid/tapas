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

func TestEditAndDeleteCredential(t *testing.T) {
	s := testutil.Vault(t)
	ctx := context.Background()
	snap, _ := s.Discover()
	secret := "synthetic-" + vault.Token()
	ref, e := s.Save(ctx, snap.Revision, metadata(), secret, "", false)
	if e != nil {
		t.Fatal(e)
	}

	snap, _ = s.Discover()
	second := metadata()
	second.Name = "Second fixture"
	secondRef, e := s.Save(ctx, snap.Revision, second, "another-secret", "", false)
	if e != nil {
		t.Fatal(e)
	}

	snap, _ = s.Discover()
	edited := metadata()
	edited.Name = "Edited fixture"
	edited.Description = "Updated without replacing the value"
	edited.Service = "other-fixture"
	edited.Environment = "staging"
	edited.SuggestedEnv = "OTHER_TOKEN"
	editedRef, e := s.Edit(ctx, snap.Revision, edited, ref)
	if e != nil || editedRef != ref {
		t.Fatal("metadata edit failed or changed the reference", editedRef, e)
	}
	m, value, e := s.Resolve(ctx, ref)
	if e != nil || m.Name != edited.Name || m.Description != edited.Description || m.Service != edited.Service || m.Environment != edited.Environment || m.SuggestedEnv != edited.SuggestedEnv || value != secret {
		t.Fatal("edit changed the wrong fields", m, e)
	}

	stale := snap.Revision
	snap, _ = s.Discover()
	if _, e = s.Edit(ctx, stale, edited, ref); !errors.Is(e, vault.ErrConflict) {
		t.Fatal("stale edit accepted", e)
	}
	duplicate := second
	duplicate.Name = edited.Name
	if _, e = s.Edit(ctx, snap.Revision, duplicate, secondRef); e == nil {
		t.Fatal("edit accepted a duplicate name")
	}

	if e = s.Delete(ctx, snap.Revision, ref); e != nil {
		t.Fatal("delete failed", e)
	}
	after, e := s.Discover()
	if e != nil || len(after.Credentials) != 1 || after.Credentials[0].ID != strings.TrimPrefix(secondRef, "store:test/") {
		t.Fatal("deleted credential remains discoverable", e)
	}
	if _, _, e = s.Resolve(ctx, ref); e == nil {
		t.Fatal("deleted credential still resolves")
	}
	if e = s.Delete(ctx, after.Revision, ref); e == nil {
		t.Fatal("missing credential was deleted twice")
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

func TestResolveReturnsExactReferenceOnly(t *testing.T) {
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
	m, value, e := s.Resolve(ctx, ref)
	if e != nil || value != secret || m.SuggestedEnv != "API_TOKEN" {
		t.Fatal("resolve returned the wrong credential", e)
	}
	if _, _, e = s.Resolve(ctx, strings.TrimPrefix(ref, "store:test/")); e != nil {
		t.Fatal("a bare identifier should resolve", e)
	}
	if _, _, e = s.Resolve(ctx, "store:other/"+m.ID); e == nil {
		t.Fatal("a foreign store name must be rejected")
	}
	if _, _, e = s.Resolve(ctx, "Fixture"); e == nil {
		t.Fatal("names must not resolve; only identifiers")
	}
	if _, _, e = s.Resolve(ctx, "../../etc/passwd"); e == nil {
		t.Fatal("invalid references must be rejected")
	}
}

func TestInitCreatesMissingStoreDirectory(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "identity.txt")
	recipient, e := vault.GenerateIdentity(key)
	if e != nil {
		t.Fatal(e)
	}
	nested := filepath.Join(dir, "config", "tapas", "vault.sops.json")
	s, e := vault.New(nested, key)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Init(context.Background(), "personal", recipient); e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(filepath.Dir(nested))
	if e != nil || info.Mode().Perm() != 0700 {
		t.Fatal("store directory missing or world readable", e)
	}
	if _, e = s.Discover(); e != nil {
		t.Fatal(e)
	}
}

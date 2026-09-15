package entry

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"tapas/internal/testutil"
	"tapas/internal/vault"
)

func start(t *testing.T, ttl time.Duration) (*Server, <-chan Result) {
	t.Helper()
	s, e := Start(testutil.Vault(t), Options{TTL: ttl, Reason: "<script>unsafe</script>"})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan Result, 1)
	go func() { out <- s.Wait(ctx) }()
	t.Cleanup(cancel)
	return s, out
}
func submit(s *Server, path, token, origin, host string, fields url.Values) *httptest.ResponseRecorder {
	if fields == nil {
		fields = url.Values{}
	}
	fields.Set("token", token)
	r := httptest.NewRequest("POST", "http://"+s.host+path, strings.NewReader(fields.Encode()))
	r.Host = host
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestEndpointSecuritySaveAndReplay(t *testing.T) {
	s, result := start(t, time.Minute)
	for _, v := range []struct{ token, origin, host string }{{"wrong", "http://" + s.host, s.host}, {s.token, "https://evil.example", s.host}, {s.token, "", s.host}, {s.token, "http://" + s.host, "evil.example"}} {
		if w := submit(s, "/save", v.token, v.origin, v.host, nil); w.Code != 403 {
			t.Fatal("invalid request accepted", w.Code)
		}
	}
	resp, e := http.Get(s.URL())
	if e != nil {
		t.Fatal(e)
	}
	html, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Referrer-Policy") != "origin" || bytes.Contains(html, []byte("<script>unsafe")) {
		t.Fatal("unsafe form response")
	}
	secret := "synthetic-" + vault.Token()
	w := submit(s, "/save", s.token, "http://"+s.host, s.host, url.Values{"name": {"User rename"}, "service": {"fixture"}, "environment": {"development"}, "value": {secret}})
	if w.Code != 200 || strings.Contains(w.Body.String(), secret) {
		t.Fatal("save failed or leaked", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Saved and encrypted") || !strings.Contains(w.Body.String(), `id="countdown" hidden>3`) || !strings.Contains(w.Body.String(), "window.opener === null") || !strings.Contains(w.Body.String(), "window.close()") {
		t.Fatal("completion page is missing its countdown")
	}
	select {
	case r := <-result:
		if r.Status != "saved" || r.Ref == "" {
			t.Fatal("wrong outcome")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not terminate")
	}
	snap, e := s.store.Discover()
	if e != nil || snap.Credentials[0].Name != "User rename" {
		t.Fatal("final name missing", e)
	}
	disk, _ := os.ReadFile(s.store.Path)
	if bytes.Contains(disk, []byte(secret)) {
		t.Fatal("plaintext persisted")
	}
	if w = submit(s, "/save", s.token, "http://"+s.host, s.host, nil); w.Code != 410 {
		t.Fatal("replay accepted")
	}
	if _, e = http.Get(s.URL()); e == nil {
		t.Fatal("listener still alive")
	}
}
func TestCancelAndExpiry(t *testing.T) {
	for _, cancel := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancel", false: "expire"}[cancel], func(t *testing.T) {
			ttl := time.Minute
			if !cancel {
				ttl = 30 * time.Millisecond
			}
			s, result := start(t, ttl)
			before, _ := os.ReadFile(s.store.Path)
			if cancel {
				if w := submit(s, "/cancel", s.token, "http://"+s.host, s.host, nil); w.Code != 200 {
					t.Fatal("cancel rejected")
				}
			}
			select {
			case r := <-result:
				want := "expired"
				if cancel {
					want = "cancelled"
				}
				if r.Status != want {
					t.Fatal("wrong terminal status")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("server did not stop")
			}
			after, _ := os.ReadFile(s.store.Path)
			if !bytes.Equal(before, after) {
				t.Fatal("cancel or expiry mutated vault")
			}
		})
	}
}
func TestReplacementRequiresConfirmation(t *testing.T) {
	s := testutil.Vault(t)
	snap, _ := s.Discover()
	m := vault.Metadata{Name: "Existing", Service: "fixture", Environment: "development"}
	if _, e := s.Save(context.Background(), snap.Revision, m, "synthetic", "", false); e != nil {
		t.Fatal(e)
	}
	snap, _ = s.Discover()
	server, e := Start(s, Options{Replace: snap.Credentials[0].ID, TTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Wait(ctx)
	fields := url.Values{"name": {"Existing"}, "service": {"fixture"}, "environment": {"development"}, "value": {"replacement"}}
	if w := submit(server, "/save", server.token, "http://"+server.host, server.host, fields); w.Code != 400 {
		t.Fatal("confirmation not enforced")
	}
	fields.Set("confirm", "yes")
	if w := submit(server, "/save", server.token, "http://"+server.host, server.host, fields); w.Code != 200 {
		t.Fatal("confirmed save failed", w.Body.String())
	}
}

func TestEditPreservesValueAndDeleteRequiresConfirmation(t *testing.T) {
	store := testutil.Vault(t)
	snap, _ := store.Discover()
	secret := "synthetic-" + vault.Token()
	ref, e := store.Save(context.Background(), snap.Revision, vault.Metadata{Name: "Existing", Service: "fixture", Environment: "development", SuggestedEnv: "API_TOKEN"}, secret, "", false)
	if e != nil {
		t.Fatal(e)
	}

	edit, e := Start(store, Options{Edit: ref, TTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	editResult := make(chan Result, 1)
	go func() { editResult <- edit.Wait(context.Background()) }()
	resp, e := http.Get(edit.URL())
	if e != nil {
		t.Fatal(e)
	}
	html, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(html, []byte("Edit credential")) || bytes.Contains(html, []byte(`name="value"`)) {
		t.Fatal("edit form exposes a secret-value field")
	}
	if w := submit(edit, "/save", edit.token, "http://"+edit.host, edit.host, url.Values{"name": {"Edited"}, "service": {"fixture"}, "environment": {"staging"}, "value": {"unexpected"}}); w.Code != http.StatusBadRequest {
		t.Fatal("metadata edit accepted a secret value", w.Code)
	}
	fields := url.Values{"name": {"Edited"}, "description": {"New details"}, "service": {"fixture"}, "environment": {"staging"}, "suggested_env": {"OTHER_TOKEN"}}
	if w := submit(edit, "/save", edit.token, "http://"+edit.host, edit.host, fields); w.Code != http.StatusOK {
		t.Fatal("edit failed", w.Code, w.Body.String())
	}
	if result := <-editResult; result.Status != "edited" || result.Ref != ref {
		t.Fatal("wrong edit result", result)
	}
	m, value, e := store.Resolve(context.Background(), ref)
	if e != nil || m.Name != "Edited" || m.Description != "New details" || m.Environment != "staging" || m.SuggestedEnv != "OTHER_TOKEN" || value != secret {
		t.Fatal("edit did not preserve the credential value", m, e)
	}

	remove, e := Start(store, Options{Delete: ref, TTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	removeResult := make(chan Result, 1)
	go func() { removeResult <- remove.Wait(context.Background()) }()
	resp, e = http.Get(remove.URL())
	if e != nil {
		t.Fatal(e)
	}
	html, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(html, []byte("Delete credential")) || !bytes.Contains(html, []byte("permanently deletes")) || bytes.Contains(html, []byte(`name="value"`)) {
		t.Fatal("delete confirmation is incomplete")
	}
	if w := submit(remove, "/delete", remove.token, "http://"+remove.host, remove.host, url.Values{"confirm": {"yes"}, "name": {"unexpected"}}); w.Code != http.StatusBadRequest {
		t.Fatal("delete accepted unexpected fields", w.Code)
	}
	if w := submit(remove, "/delete", remove.token, "http://"+remove.host, remove.host, nil); w.Code != http.StatusBadRequest {
		t.Fatal("delete did not require confirmation", w.Code)
	}
	if w := submit(remove, "/delete", remove.token, "http://"+remove.host, remove.host, url.Values{"confirm": {"yes"}}); w.Code != http.StatusOK {
		t.Fatal("confirmed delete failed", w.Code, w.Body.String())
	}
	if result := <-removeResult; result.Status != "deleted" || result.Ref != "" {
		t.Fatal("wrong delete result", result)
	}
	snap, e = store.Discover()
	if e != nil || len(snap.Credentials) != 0 {
		t.Fatal("deleted credential remains in the vault", e)
	}
}

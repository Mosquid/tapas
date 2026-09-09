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

func TestPreviewIsPartialSingleUseAndNeverReturnsFullValue(t *testing.T) {
	store := testutil.Vault(t)
	snap, _ := store.Discover()
	secret := "abcd-very-long-synthetic-secret-wxyz"
	ref, e := store.Save(context.Background(), snap.Revision, vault.Metadata{Name: "Preview me", Service: "fixture", Environment: "development", SuggestedEnv: "API_TOKEN"}, secret, "", false)
	if e != nil {
		t.Fatal(e)
	}
	s, e := StartPreview(store, ref, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	result := make(chan Result, 1)
	go func() { result <- s.Wait(context.Background()) }()

	resp, e := http.Get(s.URL())
	if e != nil {
		t.Fatal(e)
	}
	html, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || bytes.Contains(html, []byte(secret)) {
		t.Fatal("preview failed or returned the full secret")
	}
	for _, expected := range []string{"Preview me", "abcd", "wxyz", "8 of 36 characters visible", "The full value isn't here"} {
		if !bytes.Contains(html, []byte(expected)) {
			t.Fatalf("preview missing %q", expected)
		}
	}
	select {
	case got := <-result:
		if got.Status != "previewed" || got.Ref != ref {
			t.Fatal("wrong preview outcome", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("preview server did not terminate")
	}
	if _, e = http.Get(s.URL()); e == nil {
		t.Fatal("preview replay remained available")
	}
}

func TestMaskPreviewDisclosureLimit(t *testing.T) {
	for length := 1; length <= 100; length++ {
		value := strings.Repeat("x", length)
		prefix, suffix, hidden, characters := maskPreview(value)
		visible := len([]rune(prefix)) + len([]rune(suffix))
		if characters != length || hidden+visible != length || visible > 8 || visible*4 > length {
			t.Fatalf("unsafe preview sizing for length %d: visible=%d hidden=%d", length, visible, hidden)
		}
	}
	prefix, suffix, hidden, characters := maskPreview("🔑abcdef界")
	if prefix != "🔑" || suffix != "界" || hidden != 6 || characters != 8 {
		t.Fatal("preview did not count Unicode characters safely")
	}
}

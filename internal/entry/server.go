// Package entry hosts a single browser transaction, then shuts down.
package entry

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"errors"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"tapas/internal/vault"
)

//go:embed form.html
var form string

//go:embed style.css
var style string

//go:embed complete.js
var completeScript string

//go:embed complete.html
var completeForm string

var page = template.Must(template.New("entry").Parse(form))
var completePage = template.Must(template.New("complete").Parse(completeForm))

type Result struct {
	Status string `json:"status"`
	Ref    string `json:"ref,omitempty"`
	Error  string `json:"error,omitempty"`
}
type Options struct {
	Metadata vault.Metadata
	Reason   string
	Replace  string
	TTL      time.Duration
}
type Server struct {
	store           *vault.Store
	snapshot        vault.Snapshot
	options         Options
	replacementName string
	token           string
	host            string
	expires         time.Time
	mu              sync.Mutex
	finished        bool
	result          chan Result
	http            *http.Server
	listener        net.Listener
}

func Start(store *vault.Store, o Options) (*Server, error) {
	if o.TTL <= 0 || o.TTL > 5*time.Minute {
		return nil, errors.New("expiry must be greater than zero and at most five minutes")
	}
	if len(o.Reason) > 2048 {
		return nil, errors.New("purpose is too long")
	}
	snap, e := store.Discover()
	if e != nil {
		return nil, e
	}
	s := &Server{store: store, snapshot: snap, options: o, token: vault.Token(), expires: time.Now().Add(o.TTL), result: make(chan Result, 1)}
	if o.Replace != "" {
		for _, c := range snap.Credentials {
			if c.ID == o.Replace {
				s.replacementName = c.Name
				s.options.Metadata = c
			}
		}
		if s.replacementName == "" {
			return nil, errors.New("replacement ID not found")
		}
	}
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return nil, errors.New("cannot bind loopback server")
	}
	s.listener = l
	s.host = l.Addr().String()
	s.http = &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
	go func() {
		if e := s.http.Serve(l); e != nil && !errors.Is(e, http.ErrServerClosed) {
			s.finish(Result{Status: "failed", Error: "local server stopped unexpectedly"})
		}
	}()
	return s, nil
}
func (s *Server) URL() string        { return "http://" + s.host + "/?token=" + s.token }
func (s *Server) Expires() time.Time { return s.expires }
func (s *Server) finish(r Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.finished {
		s.finished = true
		s.result <- r
	}
}
func (s *Server) Wait(ctx context.Context) Result {
	timer := time.NewTimer(time.Until(s.expires))
	defer timer.Stop()
	var r Result
	select {
	case r = <-s.result:
	case <-timer.C:
		s.finish(Result{Status: "expired"})
		r = <-s.result
	case <-ctx.Done():
		s.finish(Result{Status: "cancelled"})
		r = <-s.result
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s.http.Shutdown(shutdown) != nil {
		_ = s.http.Close()
	}
	return r
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// Chrome serializes the Origin header as "null" for HTML form POSTs under
	// no-referrer. The origin policy keeps the path/query token out of Referer
	// while preserving a concrete Origin for the exact same-origin check below.
	w.Header().Set("Referrer-Policy", "origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self' 'nonce-"+s.token+"'; script-src 'nonce-"+s.token+"'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	if r.Host != s.host {
		http.Error(w, "Invalid host", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+s.host {
		http.Error(w, "Invalid origin", http.StatusForbidden)
		return
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "Cross-origin request rejected", http.StatusForbidden)
		return
	}
	if r.URL.Path == "/style.css" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		io.WriteString(w, style)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(s.token)) != 1 {
			http.Error(w, "Invalid request token", http.StatusForbidden)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.finished || time.Now().After(s.expires) {
			http.Error(w, "This request has ended", http.StatusGone)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = page.Execute(w, struct {
			Token, Reason, ReplaceName string
			Metadata                   vault.Metadata
		}{s.token, s.options.Reason, s.replacementName, s.options.Metadata})
		return
	}
	if r.Method != http.MethodPost || (r.URL.Path != "/save" && r.URL.Path != "/cancel") {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if r.Header.Get("Origin") != "http://"+s.host {
		http.Error(w, "Origin required", http.StatusForbidden)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		http.Error(w, "Unsupported content type", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if r.ParseForm() != nil {
		http.Error(w, "Invalid or oversized form", http.StatusBadRequest)
		return
	}
	for k, v := range r.PostForm {
		switch k {
		case "token", "name", "description", "service", "environment", "suggested_env", "value", "confirm":
		default:
			http.Error(w, "Unknown form field", 400)
			return
		}
		if len(v) != 1 {
			http.Error(w, "Duplicate form field", 400)
			return
		}
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("token")), []byte(s.token)) != 1 {
		http.Error(w, "Invalid request token", http.StatusForbidden)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || time.Now().After(s.expires) {
		http.Error(w, "This request has ended", http.StatusGone)
		return
	}
	result := Result{Status: "cancelled"}
	if r.URL.Path == "/save" {
		m := vault.Metadata{Name: r.PostForm.Get("name"), Description: r.PostForm.Get("description"), Service: r.PostForm.Get("service"), Environment: r.PostForm.Get("environment"), SuggestedEnv: r.PostForm.Get("suggested_env")}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		ref, e := s.store.Save(ctx, s.snapshot.Revision, m, r.PostForm.Get("value"), s.options.Replace, r.PostForm.Get("confirm") == "yes")
		if e != nil {
			http.Error(w, e.Error(), http.StatusBadRequest)
			return
		}
		result = Result{Status: "saved", Ref: ref}
	}
	s.finished = true
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	title, icon, detail := "Request canceled", "×", "Nothing was changed"
	if result.Status == "saved" {
		title, icon, detail = "Saved and encrypted", "✓", "The credential is ready"
	}
	// Inline bundled assets here: the server will be gone before a new fetch.
	_ = completePage.Execute(w, struct {
		Nonce, Title, Icon, Detail string
		Style                      template.CSS
		Script                     template.JS
	}{s.token, title, icon, detail, template.CSS(style), template.JS(completeScript)})
	// Shutdown waits for this handler to finish, including response delivery.
	s.result <- result
}

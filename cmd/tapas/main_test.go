package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"tapas/internal/testutil"
	"tapas/internal/vault"
)

// Exercise the actual CLI entry point in a subprocess without compiling a second binary.
func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv("TAPAS_TEST_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"tapas"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func TestCLIEntryLifecycle(t *testing.T) {
	s := testutil.Vault(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestCLIHelperProcess", "--", "serve", "--store", s.Path, "--identity", s.IdentityFile, "--open=false")
	cmd.Env = append(os.Environ(), "TAPAS_TEST_HELPER=1")
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = cmd.Process.Kill() }()
	scan := bufio.NewScanner(stdout)
	if !scan.Scan() {
		t.Fatal("missing ready event")
	}
	var ready struct {
		Status string `json:"status"`
		URL    string `json:"url"`
	}
	if json.Unmarshal(scan.Bytes(), &ready) != nil || ready.Status != "awaiting_user" {
		t.Fatal("server not ready")
	}
	u, e := url.Parse(ready.URL)
	if e != nil {
		t.Fatal(e)
	}
	secret := "synthetic-" + vault.Token()
	body := url.Values{"token": {u.Query().Get("token")}, "name": {"Actual saved name"}, "service": {"fixture"}, "environment": {"development"}, "value": {secret}}
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://"+u.Host+"/save", strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+u.Host)
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	page, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil || resp.StatusCode != 200 || bytes.Contains(page, []byte(secret)) {
		t.Fatal("invalid save response")
	}
	if !scan.Scan() {
		t.Fatal("missing final event")
	}
	var final struct {
		Status string `json:"status"`
		Ref    string `json:"ref"`
	}
	if bytes.Contains(scan.Bytes(), []byte(secret)) || json.Unmarshal(scan.Bytes(), &final) != nil || final.Status != "saved" || final.Ref == "" {
		t.Fatal("invalid final event")
	}
	if scan.Scan() {
		t.Fatal("unexpected additional output")
	}
	if e = cmd.Wait(); e != nil {
		t.Fatal("CLI failed")
	}
	if stderr.Len() != 0 {
		t.Fatal("unexpected diagnostic output")
	}
	snap, e := s.Discover()
	if e != nil || len(snap.Credentials) != 1 || snap.Credentials[0].Name != "Actual saved name" {
		t.Fatal("CLI did not save final metadata")
	}
	if final.Ref != "store:test/"+snap.Credentials[0].ID {
		t.Fatal("CLI returned wrong reference")
	}
}

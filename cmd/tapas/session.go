package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"tapas/internal/adapter"
	"tapas/internal/claim"
	"tapas/internal/vault"
)

// terminal reports whether a stream is attached to a screen rather than a pipe.
// Humans get a table; an agent or a script gets JSON.
func terminal(f *os.File) bool {
	info, e := f.Stat()
	return e == nil && info.Mode()&os.ModeCharDevice != 0
}

func settingsPath() (string, error) {
	home, e := os.UserHomeDir()
	if e != nil {
		return "", errors.New("cannot determine the home directory")
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

func hookInstalled() bool {
	p, e := settingsPath()
	if e != nil {
		return false
	}
	b, e := os.ReadFile(p)
	return e == nil && strings.Contains(string(b), "hook session-start")
}

// listCredentials prints vault metadata. It never decrypts.
func listCredentials(s *vault.Store, asJSON bool) error {
	snap, e := s.Discover()
	if e != nil {
		return e
	}
	if asJSON || !terminal(os.Stdout) {
		emit(snap)
		return nil
	}
	count := strconv.Itoa(len(snap.Credentials)) + " credential"
	if len(snap.Credentials) != 1 {
		count += "s"
	}
	_, _ = io.WriteString(os.Stdout, "STORE  "+snap.Store+"   "+count+"\n\n")
	if len(snap.Credentials) == 0 {
		_, _ = io.WriteString(os.Stdout, "Add one with: tapas add --name <name> --service <service> --environment <environment>\n")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = io.WriteString(w, "NAME\tSERVICE\tENVIRONMENT\tVARIABLE\tID\n")
	for _, c := range snap.Credentials {
		_, _ = io.WriteString(w, c.Name+"\t"+c.Service+"\t"+c.Environment+"\t"+c.SuggestedEnv+"\t"+c.ID+"\n")
	}
	return w.Flush()
}

func sessionID() (string, error) {
	id := os.Getenv("TAPAS_SESSION")
	if id == "" {
		return "", errors.New("no session binding; run tapas install-hooks, then start a new Claude Code session")
	}
	return id, nil
}

// requestCredentials binds references to variables for this session. It reads
// metadata without decrypting to check the reference, then decrypts once and
// discards the value, so a broken identity is reported here rather than
// silently inside a later shell preamble.
func requestCredentials(ctx context.Context, s *vault.Store, refs refList) error {
	if len(refs) == 0 {
		return errors.New("provide at least one --ref; run tapas list for exact references")
	}
	if !hookInstalled() {
		return errors.New("the SessionStart hook is not installed; run tapas install-hooks, then start a new Claude Code session")
	}
	id, e := sessionID()
	if e != nil {
		return e
	}
	snap, e := s.Discover()
	if e != nil {
		return e
	}
	project, _ := os.Getwd()
	bound := []map[string]string{}
	for _, r := range refs {
		resolveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		m, value, e := s.Resolve(resolveCtx, r.reference)
		cancel()
		if e != nil {
			return e
		}
		_ = value
		name := r.variable
		if name == "" {
			name = m.SuggestedEnv
		}
		if name == "" {
			return errors.New("credential " + m.ID + " suggests no variable; pass --ref VARIABLE=" + r.reference)
		}
		if e = vault.ValidateVariable(name); e != nil {
			return e
		}
		ref := "store:" + snap.Store + "/" + m.ID
		if e = claim.Add(id, project, claim.Claim{Variable: name, Ref: ref, Revision: snap.Revision, Created: time.Now().UTC()}); e != nil {
			return e
		}
		bound = append(bound, map[string]string{"variable": name, "ref": ref, "name": m.Name})
	}
	emit(map[string]any{"status": "claimed", "session": id, "bound": bound,
		"note": "the value reaches the next shell command; it is not redacted from that command's output"})
	return nil
}

// exportClaims writes shell assignments for the session preamble to evaluate.
// It refuses to write to a screen so a stray invocation cannot print values.
func exportClaims(ctx context.Context, s *vault.Store) error {
	if terminal(os.Stdout) {
		return errors.New("tapas env writes secrets for a shell to evaluate; it refuses to print to a terminal")
	}
	id, e := sessionID()
	if e != nil {
		return e
	}
	set, e := claim.Load(id)
	if e != nil {
		return e
	}
	for _, c := range set.Claims {
		resolveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, value, e := s.Resolve(resolveCtx, c.Ref)
		cancel()
		if e != nil {
			// A shell comment keeps the evaluated output valid.
			_, _ = io.WriteString(os.Stdout, "# tapas: "+c.Variable+" unavailable\n")
			continue
		}
		_, _ = io.WriteString(os.Stdout, adapter.Export(c.Variable, value))
	}
	return nil
}

func releaseClaims(variable string, all bool) error {
	id, e := sessionID()
	if e != nil {
		return e
	}
	if all {
		if e = claim.Clear(id); e != nil {
			return e
		}
		emit(map[string]any{"status": "released", "session": id, "all": true})
		return nil
	}
	if variable == "" {
		return errors.New("name the variable with --env, or pass --all")
	}
	found, e := claim.Remove(id, variable)
	if e != nil {
		return e
	}
	emit(map[string]any{"status": "released", "session": id, "variable": variable, "was_bound": found})
	return nil
}

// sessionStart is the SessionStart hook. It writes nothing to stdout: Claude
// Code would add that text to the model's context.
func sessionStart(executable string) error {
	var input struct {
		SessionID string `json:"session_id"`
	}
	b, e := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if e != nil || json.Unmarshal(b, &input) != nil || input.SessionID == "" {
		return errors.New("the hook received no session identifier on stdin")
	}
	claim.Prune()
	return adapter.Install(os.Getenv("CLAUDE_ENV_FILE"), executable, input.SessionID)
}

func installHooks(executable string, print bool) error {
	p, e := settingsPath()
	if e != nil {
		return e
	}
	if print {
		block, _ := json.MarshalIndent(map[string]any{"hooks": map[string]any{
			"SessionStart": []map[string]any{{"hooks": []map[string]string{{"type": "command", "command": adapter.Command(executable)}}}}}}, "", "  ")
		_, _ = os.Stdout.Write(append(block, '\n'))
		return nil
	}
	changed, e := adapter.Settings(p, executable)
	if e != nil {
		return e
	}
	emit(map[string]any{"status": "hook_installed", "settings": p, "changed": changed,
		"note": "start a new Claude Code session for the hook to take effect"})
	return nil
}

// sessionStatus reports bindings without resolving any value.
func sessionStatus() error {
	installed := hookInstalled()
	id := os.Getenv("TAPAS_SESSION")
	out := map[string]any{"hook_installed": installed, "session": id, "claims": []claim.Claim{}}
	if id != "" {
		set, e := claim.Load(id)
		if e != nil {
			return e
		}
		sort.Slice(set.Claims, func(i, j int) bool { return set.Claims[i].Variable < set.Claims[j].Variable })
		out["claims"] = set.Claims
		out["project"] = set.Project
	}
	emit(out)
	return nil
}

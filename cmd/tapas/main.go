package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tapas/internal/entry"
	"tapas/internal/runner"
	"tapas/internal/vault"
)

// version is set from the release tag with -ldflags. Development builds keep
// an explicit value so their provenance is not mistaken for a release.
var version = "dev"

func emit(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }

// childStatus carries a completed child process exit code. The child already
// reported itself, so the wrapper adds no event of its own.
type childStatus int

func (c childStatus) Error() string { return "child exited with status " + strconv.Itoa(int(c)) }

// request pairs an optional target variable with a credential reference.
// It never holds a secret value.
type request struct{ variable, reference string }

// refList collects repeated --ref values of the form REF or VARIABLE=REF.
type refList []request

func (l *refList) String() string { return "" }
func (l *refList) Set(v string) error {
	variable, reference, named := strings.Cut(v, "=")
	if !named {
		variable, reference = "", v
	}
	*l = append(*l, request{variable: variable, reference: reference})
	return nil
}

func main() {
	e := run()
	if e == nil {
		return
	}
	var status childStatus
	if errors.As(e, &status) {
		os.Exit(int(status))
	}
	emit(map[string]string{"status": "failed", "error": e.Error()})
	os.Exit(1)
}
func run() error {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		_, _ = io.WriteString(os.Stdout, "tapas "+version+"\n")
		return nil
	}
	if len(os.Args) == 2 && (os.Args[1] == "--help" || os.Args[1] == "help" || os.Args[1] == "-h") {
		_, _ = io.WriteString(os.Stdout, usage)
		return nil
	}
	if len(os.Args) < 2 {
		return errors.New("usage: tapas init|list|add|preview|run [options]; use --help for flags")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	// The personal vault is reachable from any working directory. A project that
	// wants its own vault passes --store explicitly.
	storeDefault := "vault.sops.json"
	if dir, e := os.UserConfigDir(); e == nil {
		storeDefault = filepath.Join(dir, "tapas", "vault.sops.json")
	}
	path := fs.String("store", storeDefault, "encrypted JSON path")
	name := fs.String("name", "", "logical store name (init), or suggested credential name (add)")
	recipient := fs.String("age", "", "existing age public recipient (init)")
	identity := fs.String("identity", "", "age identity path (defaults to the Agent Secrets user config directory)")
	service := fs.String("service", "", "suggested service")
	environment := fs.String("environment", "", "suggested environment")
	description := fs.String("description", "", "suggested description")
	env := fs.String("env", "", "suggested environment variable")
	reason := fs.String("reason", "", "purpose shown in the browser")
	replace := fs.String("replace", "", "exact credential ID to replace; browser confirmation required")
	ttl := fs.Duration("ttl", 5*time.Minute, "browser request lifetime, at most 5m")
	open := fs.Bool("open", true, "open the default browser")
	asJSON := fs.Bool("json", false, "print JSON even when the output is a terminal (list)")
	var refs refList
	fs.Var(&refs, "ref", "credential to deliver as VARIABLE=REF or REF (run; repeatable)")
	if e := fs.Parse(os.Args[2:]); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			fs.SetOutput(os.Stdout)
			fs.PrintDefaults()
			return nil
		}
		return errors.New("invalid arguments; use --help (secret values are accepted only in the browser)")
	}
	if fs.NArg() != 0 && os.Args[1] != "run" {
		return errors.New("unexpected positional arguments")
	}
	if *identity == "" {
		configDir, e := os.UserConfigDir()
		if e != nil {
			return errors.New("cannot determine the user config directory; provide --identity")
		}
		*identity = filepath.Join(configDir, "tapas", "age-identity.txt")
	}
	generatedIdentity := false
	if os.Args[1] == "init" && *recipient == "" {
		var e error
		*recipient, e = vault.GenerateIdentity(*identity)
		if e != nil {
			return e
		}
		generatedIdentity = true
	}
	s, e := vault.New(*path, *identity)
	if e != nil {
		if generatedIdentity {
			_ = os.Remove(*identity)
		}
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "init":
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if e = s.Init(ctx, *name, *recipient); e != nil {
			if generatedIdentity {
				_ = os.Remove(*identity)
			}
			return e
		}
		emit(map[string]any{"status": "initialized", "store": *name, "identity": *identity, "identity_created": generatedIdentity})
	case "list", "discover":
		return listCredentials(s, *asJSON)
	case "serve", "add":
		server, e := entry.Start(s, entry.Options{Metadata: vault.Metadata{Name: *name, Description: *description, Service: *service, Environment: *environment, SuggestedEnv: *env}, Reason: *reason, Replace: *replace, TTL: *ttl})
		if e != nil {
			return e
		}
		opened := false
		if *open {
			program := "xdg-open"
			if runtime.GOOS == "darwin" {
				program = "open"
			}
			openCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			cmd := exec.CommandContext(openCtx, program, server.URL())
			opened = cmd.Run() == nil
			cancel()
		}
		emit(map[string]any{"status": "awaiting_user", "url": server.URL(), "expires_at": server.Expires(), "browser_opened": opened})
		result := server.Wait(ctx)
		emit(result)
		if result.Status == "failed" {
			return errors.New("server failed")
		}
	case "preview":
		if len(refs) != 1 || refs[0].variable != "" {
			return errors.New("provide exactly one --ref REF; run tapas list for exact references")
		}
		server, e := entry.StartPreview(s, refs[0].reference, *ttl)
		if e != nil {
			return e
		}
		opened := false
		if *open {
			program := "xdg-open"
			if runtime.GOOS == "darwin" {
				program = "open"
			}
			openCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			cmd := exec.CommandContext(openCtx, program, server.URL())
			opened = cmd.Run() == nil
			cancel()
		}
		emit(map[string]any{"status": "awaiting_user", "url": server.URL(), "expires_at": server.Expires(), "browser_opened": opened})
		result := server.Wait(ctx)
		emit(result)
		if result.Status == "failed" {
			return errors.New("server failed")
		}
	case "run":
		if len(refs) == 0 {
			return errors.New("provide at least one --ref; run tapas list for exact references")
		}
		// Reject an unusable target before decrypting anything.
		for _, r := range refs {
			if r.variable != "" {
				if e := vault.ValidateVariable(r.variable); e != nil {
					return e
				}
			}
		}
		bindings := make([]runner.Binding, 0, len(refs))
		for _, r := range refs {
			resolveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			m, value, e := s.Resolve(resolveCtx, r.reference)
			cancel()
			if e != nil {
				return e
			}
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
			for _, b := range bindings {
				if b.Variable == name {
					return errors.New("two credentials target " + name + "; name each one explicitly")
				}
			}
			bindings = append(bindings, runner.Binding{Variable: name, Value: value})
		}
		status, e := runner.Exec(ctx, fs.Args(), bindings, os.Stdout, os.Stderr)
		if e != nil {
			return e
		}
		if status != 0 {
			return childStatus(status)
		}
	default:
		return errors.New("unknown command; use init, list, add, preview, or run")
	}
	return nil
}

const usage = `Usage: tapas <command> [options]

  version  Print the installed version

Vault
  init   Create an identity and encrypted JSON vault
  list   Show credential metadata; never decrypts (alias: discover)
  add    Open a single-use browser form, save, and exit (alias: serve)
  preview  Open a single-use masked credential preview in the browser

Using a credential
  run    Run one command with credentials in its environment only

Use tapas <command> --help for options.
Secret values are accepted only in the browser. Decrypted values and previews
are never printed by the CLI or included in an error message.
`

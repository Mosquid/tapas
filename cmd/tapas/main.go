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
	"syscall"
	"time"

	"tapas/internal/entry"
	"tapas/internal/vault"
)

func emit(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }
func main() {
	if e := run(); e != nil {
		emit(map[string]string{"status": "failed", "error": e.Error()})
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) == 2 && (os.Args[1] == "--help" || os.Args[1] == "help" || os.Args[1] == "-h") {
		_, _ = io.WriteString(os.Stdout, "Usage: tapas <command> [options]\n\nCommands:\n  init       Create an identity and encrypted JSON vault\n  discover   List metadata without decrypting values\n  serve      Open a single-use browser form, save, and exit\n\nUse tapas <command> --help for options. Secret values are accepted only in the browser.\n")
		return nil
	}
	if len(os.Args) < 2 {
		return errors.New("usage: tapas init|discover|serve [options]; use --help for flags")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("store", "vault.sops.json", "encrypted JSON path")
	name := fs.String("name", "", "logical store name (init), or suggested credential name (serve)")
	recipient := fs.String("age", "", "existing age public recipient (init)")
	identity := fs.String("identity", "", "age identity path (defaults to the Agent Secrets user config directory)")
	service := fs.String("service", "", "suggested service")
	environment := fs.String("environment", "", "suggested environment")
	description := fs.String("description", "", "suggested description")
	env := fs.String("env", "", "suggested environment variable")
	reason := fs.String("reason", "", "purpose shown in the browser")
	replace := fs.String("replace", "", "exact credential ID to replace; browser confirmation required")
	ttl := fs.Duration("ttl", 5*time.Minute, "form lifetime, at most 5m")
	open := fs.Bool("open", true, "open the default browser")
	if e := fs.Parse(os.Args[2:]); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			fs.SetOutput(os.Stdout)
			fs.PrintDefaults()
			return nil
		}
		return errors.New("invalid arguments; use --help (secret values are accepted only in the browser)")
	}
	if fs.NArg() != 0 {
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
	case "discover":
		snap, e := s.Discover()
		if e != nil {
			return e
		}
		emit(snap)
	case "serve":
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
	default:
		return errors.New("unknown command; use init, discover, or serve")
	}
	return nil
}

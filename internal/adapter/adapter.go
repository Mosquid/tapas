// Package adapter connects the vault to the Claude Code shell lifecycle. A
// SessionStart hook writes a preamble that resolves this session's claims
// before each Bash command, so no decrypted export is ever stored on disk.
package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const marker = "# agent secrets: resolve this session's claims"

// Command is the hook invocation written into the user settings file.
func Command(executable string) string { return quote(executable) + " hook session-start" }

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Preamble returns the lines appended to CLAUDE_ENV_FILE. It exports the
// session identifier and a resolver invocation, never a credential value. The
// resolver runs again before every Bash command, so a claim made later in the
// session takes effect without reinstalling anything.
func Preamble(executable, session string) string {
	return marker + "\n" +
		"export TAPAS_SESSION=" + quote(session) + "\n" +
		`eval "$(` + quote(executable) + ` env 2>/dev/null)"` + "\n"
}

// Install appends the preamble to the file named by CLAUDE_ENV_FILE. It appends
// rather than truncates, so a direnv or devbox preamble already in that file
// survives. Repeated session starts do not duplicate the block.
func Install(envFile, executable, session string) error {
	if envFile == "" {
		return errors.New("CLAUDE_ENV_FILE is not set; this is not a Claude Code session start")
	}
	existing, e := os.ReadFile(envFile)
	if e != nil && !os.IsNotExist(e) {
		return errors.New("cannot read the session environment file")
	}
	if bytes.Contains(existing, []byte(marker)) {
		return nil
	}
	f, e := os.OpenFile(envFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if e != nil {
		return errors.New("cannot open the session environment file")
	}
	defer f.Close()
	prefix := ""
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		prefix = "\n"
	}
	if _, e = f.WriteString(prefix + Preamble(executable, session)); e != nil {
		return errors.New("cannot write the session environment file")
	}
	return nil
}

// Settings adds the SessionStart hook to a Claude Code settings file, keeping
// every other key and every unrelated hook exactly as it was. It reports
// whether the file changed.
func Settings(path, executable string) (bool, error) {
	top := map[string]json.RawMessage{}
	existing, e := os.ReadFile(path)
	if e != nil && !os.IsNotExist(e) {
		return false, errors.New("cannot read the settings file")
	}
	if len(existing) > 0 {
		if json.Unmarshal(existing, &top) != nil {
			return false, errors.New("settings file is not a JSON object; fix it before installing")
		}
	}
	events := map[string]json.RawMessage{}
	if raw, ok := top["hooks"]; ok {
		if json.Unmarshal(raw, &events) != nil {
			return false, errors.New("the hooks setting is not a JSON object; install by hand")
		}
	}
	groups := []json.RawMessage{}
	if raw, ok := events["SessionStart"]; ok {
		if json.Unmarshal(raw, &groups) != nil {
			return false, errors.New("the SessionStart setting is not a JSON array; install by hand")
		}
	}
	for _, group := range groups {
		if strings.Contains(string(group), "hook session-start") {
			return false, nil
		}
	}
	added, _ := json.Marshal(map[string]any{"hooks": []map[string]string{{"type": "command", "command": Command(executable)}}})
	groups = append(groups, added)
	events["SessionStart"], _ = json.Marshal(groups)
	top["hooks"], _ = json.Marshal(events)
	out, e := json.MarshalIndent(top, "", "  ")
	if e != nil {
		return false, errors.New("cannot encode the settings file")
	}
	if len(existing) > 0 {
		if e = os.WriteFile(path+".tapas-backup", existing, 0600); e != nil {
			return false, errors.New("cannot back up the settings file")
		}
	}
	if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return false, errors.New("cannot create the settings directory")
	}
	if e = os.WriteFile(path, append(out, '\n'), 0644); e != nil {
		return false, errors.New("cannot write the settings file")
	}
	return true, nil
}

// Export renders one shell assignment for the preamble to evaluate.
func Export(variable, value string) string { return "export " + variable + "=" + quote(value) + "\n" }

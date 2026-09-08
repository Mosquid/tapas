// Package claim records which credentials a session has bound to which
// environment variables. It stores references and metadata only, never values.
// Claims are scoped to one session and expire when that session is gone.
package claim

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// MaxAge bounds how long an abandoned session's claims stay on disk. Sessions
// do not announce their end, so old files are pruned by age instead.
const MaxAge = 7 * 24 * time.Hour

var session = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

type Claim struct {
	Variable string    `json:"variable"`
	Ref      string    `json:"ref"`
	Revision string    `json:"revision"`
	Created  time.Time `json:"created"`
}
type Set struct {
	Session string  `json:"session"`
	Project string  `json:"project"`
	Claims  []Claim `json:"claims"`
}

func Dir() (string, error) {
	config, e := os.UserConfigDir()
	if e != nil {
		return "", errors.New("cannot determine the user config directory")
	}
	return filepath.Join(config, "tapas", "claims"), nil
}

func path(id string) (string, error) {
	if !session.MatchString(id) {
		return "", errors.New("invalid session identifier")
	}
	dir, e := Dir()
	if e != nil {
		return "", e
	}
	return filepath.Join(dir, id+".json"), nil
}

func Load(id string) (Set, error) {
	p, e := path(id)
	if e != nil {
		return Set{}, e
	}
	b, e := os.ReadFile(p)
	if os.IsNotExist(e) {
		return Set{Session: id}, nil
	}
	if e != nil {
		return Set{}, errors.New("cannot read session claims")
	}
	var s Set
	if json.Unmarshal(b, &s) != nil {
		return Set{}, errors.New("session claims are unreadable; release them and claim again")
	}
	s.Session = id
	return s, nil
}

func store(s Set) error {
	p, e := path(s.Session)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return errors.New("cannot create the claims directory")
	}
	b, _ := json.Marshal(s)
	f, e := os.CreateTemp(filepath.Dir(p), ".tapas-claims-*")
	if e != nil {
		return errors.New("cannot stage session claims")
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e != nil {
		f.Close()
		return errors.New("cannot secure session claims")
	}
	if _, e = f.Write(b); e != nil {
		f.Close()
		return errors.New("cannot write session claims")
	}
	if f.Sync() != nil || f.Close() != nil {
		return errors.New("cannot flush session claims")
	}
	if os.Rename(f.Name(), p) != nil {
		return errors.New("cannot commit session claims")
	}
	return nil
}

// Add binds one variable. A second claim on the same variable replaces the
// first, so a rotated credential does not leave a stale binding behind.
func Add(id, project string, c Claim) error {
	s, e := Load(id)
	if e != nil {
		return e
	}
	s.Project = project
	for i, existing := range s.Claims {
		if existing.Variable == c.Variable {
			s.Claims[i] = c
			return store(s)
		}
	}
	s.Claims = append(s.Claims, c)
	return store(s)
}

func Remove(id, variable string) (bool, error) {
	s, e := Load(id)
	if e != nil {
		return false, e
	}
	kept := s.Claims[:0]
	found := false
	for _, c := range s.Claims {
		if c.Variable == variable {
			found = true
			continue
		}
		kept = append(kept, c)
	}
	s.Claims = kept
	return found, store(s)
}

func Clear(id string) error {
	p, e := path(id)
	if e != nil {
		return e
	}
	if e = os.Remove(p); e != nil && !os.IsNotExist(e) {
		return errors.New("cannot release session claims")
	}
	return nil
}

// Prune removes claim files left behind by sessions that ended without a
// release. It never reports an error: pruning is maintenance, not the caller's
// task.
func Prune() {
	dir, e := Dir()
	if e != nil {
		return
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return
	}
	for _, entry := range entries {
		info, e := entry.Info()
		if e == nil && !info.IsDir() && time.Since(info.ModTime()) > MaxAge {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

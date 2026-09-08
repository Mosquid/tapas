// Package vault maintains a strict JSON store through the external SOPS binary.
// Plaintext exists only in memory and subprocess pipes.
package vault

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
)

const metadataPattern = "^(schema|store|recipient|id|name|description|service|environment|suggested_env)$"
const maxStore = 4 << 20

var ErrConflict = errors.New("store changed; reopen the form and try again")
var ErrDecrypt = errors.New("SOPS decryption failed; check your age identity and store integrity")
var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var variable = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

type Metadata struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Service      string `json:"service"`
	Environment  string `json:"environment"`
	SuggestedEnv string `json:"suggested_env,omitempty"`
}
type credential struct {
	Metadata
	Value string `json:"value"`
}
type document struct {
	Schema      int             `json:"schema"`
	Store       string          `json:"store"`
	Recipient   string          `json:"recipient"`
	Credentials []credential    `json:"credentials"`
	SOPS        json.RawMessage `json:"sops,omitempty"`
}
type Snapshot struct {
	Store       string     `json:"store"`
	Revision    string     `json:"revision"`
	Credentials []Metadata `json:"credentials"`
}
type Store struct {
	Path         string
	Binary       string
	IdentityFile string
}

func New(path string, identityFile ...string) (*Store, error) {
	p, e := filepath.Abs(path)
	if e != nil {
		return nil, errors.New("invalid store path")
	}
	if filepath.Ext(p) != ".json" {
		return nil, errors.New("this slice supports JSON stores; use a .json path")
	}
	b, e := exec.LookPath("sops")
	if e != nil {
		return nil, errors.New("SOPS is not installed")
	}
	s := &Store{Path: p, Binary: b}
	if len(identityFile) > 1 {
		return nil, errors.New("only one age identity file is supported")
	}
	if len(identityFile) == 1 && identityFile[0] != "" {
		s.IdentityFile, e = filepath.Abs(identityFile[0])
		if e != nil {
			return nil, errors.New("invalid identity path")
		}
	}
	return s, nil
}

// GenerateIdentity creates an age identity with owner-only permissions. The
// private identity is never returned; callers receive only its public recipient.
func GenerateIdentity(path string) (string, error) {
	p, e := filepath.Abs(path)
	if e != nil {
		return "", errors.New("invalid identity path")
	}
	if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return "", errors.New("cannot create identity directory")
	}
	id, e := age.GenerateX25519Identity()
	if e != nil {
		return "", errors.New("cannot generate age identity")
	}
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return "", errors.New("identity already exists or cannot be created")
	}
	data := []byte("# created by Agent Secrets; keep this private and back it up\n" + id.String() + "\n")
	if _, e = f.Write(data); e == nil {
		e = f.Sync()
	}
	closeError := f.Close()
	if e != nil || closeError != nil {
		_ = os.Remove(p)
		return "", errors.New("cannot write age identity")
	}
	return id.Recipient().String(), nil
}
func Token() string {
	var b [24]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("random source unavailable")
	}
	return hex.EncodeToString(b[:])
}
func Revision(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (m Metadata) Validate() error {
	if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Service) == "" || strings.TrimSpace(m.Environment) == "" {
		return errors.New("name, service, and environment are required")
	}
	for _, v := range []string{m.Name, m.Description, m.Service, m.Environment, m.SuggestedEnv} {
		if len(v) > 1024 || strings.ContainsRune(v, 0) {
			return errors.New("metadata is invalid or too long")
		}
	}
	n := m.SuggestedEnv
	if n != "" && (!variable.MatchString(n) || len(n) > 128 || n == "PATH" || n == "HOME" || n == "ENV" || n == "BASH_ENV" || n == "SHELLOPTS" || n == "BASHOPTS" || n == "CDPATH" || n == "IFS" || strings.HasPrefix(n, "LD_") || strings.HasPrefix(n, "DYLD_") || strings.HasPrefix(n, "PYTHON") || strings.HasPrefix(n, "NODE_") || strings.HasPrefix(n, "SOPS_") || strings.HasPrefix(n, "TAPAS_")) {
		return errors.New("unsupported environment variable name")
	}
	return nil
}

// Reject duplicate keys before decoding; encoding/json otherwise accepts them.
func unique(d *json.Decoder) error {
	t, e := d.Token()
	if e != nil {
		return e
	}
	v, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch v {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			s := k.(string)
			if seen[s] {
				return errors.New("duplicate key")
			}
			seen[s] = true
			if e = unique(d); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e = unique(d); e != nil {
				return e
			}
		}
	default:
		return errors.New("invalid JSON")
	}
	_, e = d.Token()
	return e
}
func decode(b []byte, encrypted bool) (document, error) {
	var doc document
	invalid := errors.New("invalid vault schema or encryption policy")
	d := json.NewDecoder(bytes.NewReader(b))
	if unique(d) != nil {
		return doc, invalid
	}
	if _, e := d.Token(); e != io.EOF {
		return doc, invalid
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&doc) != nil {
		return doc, invalid
	}
	if doc.Schema != 1 || !identifier.MatchString(doc.Store) || !strings.HasPrefix(doc.Recipient, "age1") {
		return doc, invalid
	}
	ids := map[string]bool{}
	names := map[string]bool{}
	for _, c := range doc.Credentials {
		if !identifier.MatchString(c.ID) || ids[c.ID] || names[c.Name] || c.Metadata.Validate() != nil {
			return doc, invalid
		}
		ids[c.ID] = true
		names[c.Name] = true
		if encrypted && !strings.HasPrefix(c.Value, "ENC[AES256_GCM,") {
			return doc, invalid
		}
	}
	if encrypted {
		var s struct {
			UnencryptedRegex string `json:"unencrypted_regex"`
			Age              []struct {
				Recipient string `json:"recipient"`
			} `json:"age"`
		}
		if json.Unmarshal(doc.SOPS, &s) != nil || s.UnencryptedRegex != metadataPattern || len(s.Age) != 1 || s.Age[0].Recipient != doc.Recipient {
			return doc, invalid
		}
		// This first slice supports exactly one age recipient. Refuse policies
		// that re-encryption would otherwise silently strip or change.
		var policy map[string]json.RawMessage
		if json.Unmarshal(doc.SOPS, &policy) != nil {
			return doc, invalid
		}
		for _, key := range []string{"kms", "gcp_kms", "azure_kv", "hc_vault", "pgp", "key_groups"} {
			if raw, ok := policy[key]; ok && string(raw) != "null" {
				var values []json.RawMessage
				if json.Unmarshal(raw, &values) != nil || len(values) != 0 {
					return doc, invalid
				}
			}
		}
		for _, key := range []string{"encrypted_regex", "encrypted_suffix", "unencrypted_suffix", "unencrypted_comment_regex", "encrypted_comment_regex"} {
			if raw, ok := policy[key]; ok && string(raw) != `""` {
				return doc, invalid
			}
		}
	}
	return doc, nil
}
func (s *Store) read() ([]byte, error) {
	fd, e := syscall.Open(s.Path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, errors.New("store unavailable; initialize it or check its path")
	}
	f := os.NewFile(uintptr(fd), "store")
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, errors.New("cannot inspect store")
	}
	if !st.Mode().IsRegular() || st.Size() > maxStore {
		return nil, errors.New("store must be a regular JSON file under 4 MiB")
	}
	b, e := io.ReadAll(io.LimitReader(f, maxStore+1))
	if e != nil {
		return nil, errors.New("cannot read store")
	}
	if len(b) > maxStore {
		return nil, errors.New("store exceeds 4 MiB")
	}
	return b, nil
}
func (s *Store) Discover() (Snapshot, error) {
	b, e := s.read()
	if e != nil {
		return Snapshot{}, e
	}
	d, e := decode(b, true)
	if e != nil {
		return Snapshot{}, e
	}
	out := Snapshot{Store: d.Store, Revision: Revision(b), Credentials: []Metadata{}}
	for _, c := range d.Credentials {
		out.Credentials = append(out.Credentials, c.Metadata)
	}
	sort.Slice(out.Credentials, func(i, j int) bool { return out.Credentials[i].Name < out.Credentials[j].Name })
	return out, nil
}
func (s *Store) crypt(ctx context.Context, b []byte, recipient string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"--input-type", "json", "--output-type", "json", "--config", "/dev/null"}
	if recipient == "" {
		args = append(args, "--decrypt")
	} else {
		args = append(args, "--encrypt", "--age", recipient, "--unencrypted-regex", metadataPattern)
	}
	args = append(args, "/dev/stdin")
	cmd := exec.CommandContext(ctx, s.Binary, args...)
	cmd.Stdin = bytes.NewReader(b)
	// Never inherit recipient-selection variables, and never forward SOPS diagnostics.
	for _, v := range os.Environ() {
		k, _, _ := strings.Cut(v, "=")
		switch k {
		case "SOPS_AGE_KEY", "SOPS_AGE_KEY_FILE", "SOPS_AGE_RECIPIENTS", "SOPS_KMS_ARN", "SOPS_PGP_FP", "SOPS_GCP_KMS_IDS", "SOPS_AZURE_KEYVAULT_URL", "SOPS_VAULT_URIS":
			continue
		}
		cmd.Env = append(cmd.Env, v)
	}
	if s.IdentityFile != "" {
		cmd.Env = append(cmd.Env, "SOPS_AGE_KEY_FILE="+s.IdentityFile)
	}
	out, e := cmd.Output()
	if e != nil {
		if recipient == "" {
			return nil, ErrDecrypt
		}
		return nil, errors.New("SOPS encryption failed; check recipient configuration")
	}
	return out, nil
}
func (s *Store) lock(ctx context.Context) (func(), error) {
	fd, e := syscall.Open(s.Path+".lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, errors.New("cannot open store lock")
	}
	f := os.NewFile(uintptr(fd), "store-lock")
	for {
		e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			return func() { _ = syscall.Flock(fd, syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if e != syscall.EWOULDBLOCK && e != syscall.EAGAIN {
			f.Close()
			return nil, errors.New("cannot lock store")
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, errors.New("store lock timed out")
		case <-time.After(25 * time.Millisecond):
		}
	}
}
func (s *Store) commit(b []byte, create bool) error {
	if len(b) > maxStore {
		return errors.New("encrypted store would exceed 4 MiB")
	}
	f, e := os.CreateTemp(filepath.Dir(s.Path), ".tapas-encrypted-*")
	if e != nil {
		return errors.New("cannot stage encrypted store")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, e = f.Write(b); e != nil {
		return errors.New("cannot write encrypted store")
	}
	if f.Sync() != nil || f.Close() != nil {
		return errors.New("cannot flush encrypted store")
	}
	if create {
		e = os.Link(f.Name(), s.Path)
	} else {
		e = os.Rename(f.Name(), s.Path)
	}
	if e != nil {
		return errors.New("cannot commit store; original retained")
	}
	dir, e := os.Open(filepath.Dir(s.Path))
	if e != nil {
		return errors.New("store committed but durability could not be verified")
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return errors.New("store committed but durability could not be verified")
	}
	return nil
}
func (s *Store) Init(ctx context.Context, name, recipient string) error {
	if !identifier.MatchString(name) || !strings.HasPrefix(recipient, "age1") {
		return errors.New("provide a logical store name and an age public recipient")
	}
	unlock, e := s.lock(ctx)
	if e != nil {
		return e
	}
	defer unlock()
	if _, e = os.Lstat(s.Path); !os.IsNotExist(e) {
		return errors.New("store already exists or is inaccessible")
	}
	d := document{Schema: 1, Store: name, Recipient: recipient, Credentials: []credential{}}
	plain, _ := json.Marshal(d)
	b, e := s.crypt(ctx, plain, recipient)
	if e != nil {
		return e
	}
	if _, e = decode(b, true); e != nil {
		return e
	}
	if _, e = s.crypt(ctx, b, ""); e != nil {
		return e
	}
	return s.commit(b, true)
}

// Save checks the revision captured when the browser opened. Replacement requires
// both an exact ID and explicit browser confirmation. No raw values are returned.
func (s *Store) Save(ctx context.Context, revision string, m Metadata, value, replace string, confirmed bool) (string, error) {
	if e := m.Validate(); e != nil {
		return "", e
	}
	if len(value) == 0 || len(value) > 65536 || strings.ContainsRune(value, 0) {
		return "", errors.New("secret must contain 1–65536 bytes and no NUL characters")
	}
	unlock, e := s.lock(ctx)
	if e != nil {
		return "", e
	}
	defer unlock()
	b, e := s.read()
	if e != nil {
		return "", e
	}
	if Revision(b) != revision {
		return "", ErrConflict
	}
	if _, e = decode(b, true); e != nil {
		return "", e
	}
	plain, e := s.crypt(ctx, b, "")
	if e != nil {
		return "", e
	}
	d, e := decode(plain, false)
	if e != nil {
		return "", e
	}
	idx := -1
	for i, c := range d.Credentials {
		if c.ID == replace {
			idx = i
		}
		if c.Name == m.Name && c.ID != replace {
			return "", errors.New("name already exists; explicitly request replacement")
		}
	}
	if replace != "" {
		if idx < 0 {
			return "", errors.New("replacement entry not found")
		}
		if !confirmed {
			return "", errors.New("confirm replacement in the browser")
		}
		m.ID = replace
	} else {
		m.ID = Token()
	}
	c := credential{Metadata: m, Value: value}
	if idx >= 0 {
		d.Credentials[idx] = c
	} else {
		d.Credentials = append(d.Credentials, c)
	}
	d.SOPS = nil
	plain, _ = json.Marshal(d)
	encrypted, e := s.crypt(ctx, plain, d.Recipient)
	if e != nil {
		return "", e
	}
	if _, e = decode(encrypted, true); e != nil {
		return "", e
	}
	check, e := s.crypt(ctx, encrypted, "")
	if e != nil {
		return "", e
	}
	verified, e := decode(check, false)
	if e != nil || !reflect.DeepEqual(verified, d) {
		return "", errors.New("encrypted store validation failed")
	}
	latest, e := s.read()
	if e != nil {
		return "", e
	}
	if Revision(latest) != revision {
		return "", ErrConflict
	}
	if e = s.commit(encrypted, false); e != nil {
		return "", e
	}
	return "store:" + d.Store + "/" + m.ID, nil
}

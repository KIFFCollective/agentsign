// Package store manages per-agent material on disk:
//
//	<root>/<agent-id>/
//	  agent.toml    name, email, allowed hosts and repos
//	  signing_key   OpenSSH ed25519 private key, 0600
//	  token         fine-grained token, 0600 (or token_file in agent.toml)
package store

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"golang.org/x/crypto/ssh"

	"github.com/KIFFCollective/agentsign/internal/resolve"
)

const (
	AgentFile      = "agent.toml"
	SigningKeyFile = "signing_key"
	TokenFile      = "token"
)

// ErrNotFound means the agent has no folder in the store.
var ErrNotFound = errors.New("agent not found")

// Agent is agent.toml.
type Agent struct {
	Name  string `toml:"name"`
	Email string `toml:"email"`

	// Username sent with the token. GitHub ignores it for fine-grained
	// tokens, but it shows up in server logs; the machine account login is a
	// good choice.
	Username string `toml:"username,omitempty"`

	// Hosts the token may be sent to. Defaults to ["github.com"].
	Hosts []string `toml:"hosts,omitempty"`

	// Repos ("owner/name" or "owner/*") the token may be sent for. Empty
	// means any repo on the allowed hosts; the token's own scope still applies.
	Repos []string `toml:"repos,omitempty"`

	// TokenFile points at a token kept outside the store (e.g. a mounted
	// secret). Relative paths are relative to the agent folder.
	TokenFile string `toml:"token_file,omitempty"`

	// TokenExpiry is when the token expires, reported to git as
	// password_expiry_utc and checked by doctor.
	TokenExpiry *time.Time `toml:"token_expiry,omitempty"`

	Revoked   bool       `toml:"revoked,omitempty"`
	RevokedAt *time.Time `toml:"revoked_at,omitempty"`

	// Set on load, not stored.
	ID  string `toml:"-"`
	Dir string `toml:"-"`
}

// Store is an agent store rooted at a folder (already {agent}-expanded).
type Store struct {
	Root string
}

// New returns a store rooted at root.
func New(root string) *Store { return &Store{Root: root} }

func (s *Store) dir(id string) (string, error) {
	if !resolve.ValidID(id) {
		return "", fmt.Errorf("invalid agent id %q", id)
	}
	return filepath.Join(s.Root, id), nil
}

// Load reads an agent's agent.toml.
func (s *Store) Load(id string) (*Agent, error) {
	dir, err := s.dir(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, AgentFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s (store %s)", ErrNotFound, id, s.Root)
	}
	if err != nil {
		return nil, err
	}
	var a Agent
	md, err := toml.Decode(string(data), &a)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, AgentFile), err)
	}
	if un := md.Undecoded(); len(un) > 0 {
		return nil, fmt.Errorf("%s: unknown key %q", filepath.Join(dir, AgentFile), un[0].String())
	}
	a.ID, a.Dir = id, dir
	if len(a.Hosts) == 0 {
		a.Hosts = []string{"github.com"}
	}
	return &a, nil
}

// List returns all agents in the store, sorted by id. Folders without a
// readable agent.toml are skipped.
func (s *Store) List() ([]*Agent, error) {
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Agent
	for _, e := range entries {
		if !e.IsDir() || !resolve.ValidID(e.Name()) {
			continue
		}
		a, err := s.Load(e.Name())
		if err != nil {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Save writes agent.toml.
func (s *Store) Save(a *Agent) error {
	dir, err := s.dir(a.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# agentsign agent: " + a.ID + "\n")
	if err := toml.NewEncoder(&b).Encode(a); err != nil {
		return err
	}
	a.Dir = dir
	return writeFileAtomic(filepath.Join(dir, AgentFile), []byte(b.String()), 0o600)
}

// Create makes a new agent with a fresh signing key. It fails if the agent
// already exists.
func (s *Store) Create(a *Agent) error {
	dir, err := s.dir(a.ID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, AgentFile)); err == nil {
		return fmt.Errorf("agent %s already exists", a.ID)
	}
	if err := s.Save(a); err != nil {
		return err
	}
	return s.GenerateKey(a)
}

// GenerateKey writes a new ed25519 signing key, replacing any existing one.
// The previous key, if any, is kept as signing_key.old until the next rotate.
func (s *Store) GenerateKey(a *Agent) error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, a.Email)
	if err != nil {
		return err
	}
	path := a.KeyPath()
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, path+".old"); err != nil {
			return err
		}
	}
	return writeFileAtomic(path, pem.EncodeToMemory(block), 0o600)
}

// KeyPath is the agent's private signing key file.
func (a *Agent) KeyPath() string { return filepath.Join(a.Dir, SigningKeyFile) }

// PublicKey returns the agent's public key in authorized_keys form, with the
// email as comment.
func (a *Agent) PublicKey() (string, error) {
	signer, err := a.signer()
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	return line + " " + a.Email, nil
}

// Fingerprint returns the SHA256 fingerprint of the agent's key.
func (a *Agent) Fingerprint() (string, error) {
	signer, err := a.signer()
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(signer.PublicKey()), nil
}

func (a *Agent) signer() (ssh.Signer, error) {
	data, err := os.ReadFile(a.KeyPath())
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(data)
}

// tokenPath returns where the token lives.
func (a *Agent) tokenPath() string {
	if a.TokenFile == "" {
		return filepath.Join(a.Dir, TokenFile)
	}
	if filepath.IsAbs(a.TokenFile) {
		return a.TokenFile
	}
	return filepath.Join(a.Dir, a.TokenFile)
}

// Token reads the agent's token.
func (a *Agent) Token() (string, error) {
	data, err := os.ReadFile(a.tokenPath())
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("token file %s is empty", a.tokenPath())
	}
	if strings.ContainsAny(tok, "\r\n\x00") {
		return "", fmt.Errorf("token file %s contains more than one line", a.tokenPath())
	}
	return tok, nil
}

// SetToken stores a token in the agent folder.
func (s *Store) SetToken(a *Agent, token string, expiry *time.Time) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("empty token")
	}
	if strings.ContainsAny(token, "\r\n\x00 \t") {
		return errors.New("token must be a single word")
	}
	if a.TokenFile != "" {
		return fmt.Errorf("agent %s uses token_file %s; update that file instead", a.ID, a.TokenFile)
	}
	if err := writeFileAtomic(filepath.Join(a.Dir, TokenFile), []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	if expiry != nil {
		a.TokenExpiry = expiry
		return s.Save(a)
	}
	return nil
}

// Revoke deletes the agent's key and token and marks it revoked. agent.toml
// is kept so the agent's history stays explainable.
func (s *Store) Revoke(a *Agent) error {
	var errs []error
	for _, p := range []string{a.KeyPath(), a.KeyPath() + ".old", filepath.Join(a.Dir, TokenFile)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	a.Revoked, a.RevokedAt = true, &now
	errs = append(errs, s.Save(a))
	return errors.Join(errs...)
}

// AllowsHost reports whether the token may be sent to host.
func (a *Agent) AllowsHost(host string) bool {
	host = strings.ToLower(host)
	for _, h := range a.Hosts {
		if strings.ToLower(h) == host {
			return true
		}
	}
	return false
}

// AllowsRepo reports whether the token may be sent for repo ("owner/name",
// optionally with .git). An empty Repos list allows any repo; an entry
// "owner/*" allows every repo of that owner.
func (a *Agent) AllowsRepo(repo string) bool {
	if len(a.Repos) == 0 {
		return true
	}
	return a.MatchingRepo(repo) != ""
}

// MatchingRepo returns the Repos entry that allows repo, or "".
func (a *Agent) MatchingRepo(repo string) string {
	repo = normRepo(repo)
	for _, r := range a.Repos {
		if matchRepo(normRepo(r), repo) {
			return r
		}
	}
	return ""
}

func matchRepo(pattern, repo string) bool {
	if pattern == repo {
		return true
	}
	owner, ok := strings.CutSuffix(pattern, "/*")
	if !ok {
		return false
	}
	name, ok := strings.CutPrefix(repo, owner+"/")
	return ok && name != "" && !strings.Contains(name, "/")
}

func normRepo(r string) string {
	r = strings.Trim(r, "/")
	r = strings.TrimSuffix(r, ".git")
	return strings.ToLower(r)
}

var repoPart = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// CleanRepo validates a repo entry and returns it in canonical form
// ("owner/name" or "owner/*", without slashes at the ends or .git). Case is
// kept for display; matching ignores it.
func CleanRepo(r string) (string, error) {
	c := strings.TrimSuffix(strings.Trim(strings.TrimSpace(r), "/"), ".git")
	owner, name, ok := strings.Cut(c, "/")
	if !ok || strings.Contains(name, "/") || !repoPart.MatchString(owner) || (name != "*" && !repoPart.MatchString(name)) {
		return "", fmt.Errorf("repo %q must be owner/name or owner/* (leave the list empty for all repos)", r)
	}
	return c, nil
}

// AddRepos adds entries to Repos, skipping ones already listed. It returns
// the entries that were added.
func (a *Agent) AddRepos(repos ...string) ([]string, error) {
	var added []string
	for _, r := range repos {
		c, err := CleanRepo(r)
		if err != nil {
			return nil, err
		}
		if a.hasRepo(c) {
			continue
		}
		a.Repos = append(a.Repos, c)
		added = append(added, c)
	}
	return added, nil
}

// RemoveRepos removes exact entries from Repos. It returns the removed
// entries and the ones that weren't listed.
func (a *Agent) RemoveRepos(repos ...string) (removed, missing []string, err error) {
	drop := map[string]bool{}
	for _, r := range repos {
		c, err := CleanRepo(r)
		if err != nil {
			return nil, nil, err
		}
		if a.hasRepo(c) {
			drop[normRepo(c)] = true
		} else {
			missing = append(missing, c)
		}
	}
	kept := a.Repos[:0:0]
	for _, r := range a.Repos {
		if drop[normRepo(r)] {
			removed = append(removed, r)
		} else {
			kept = append(kept, r)
		}
	}
	a.Repos = kept
	return removed, missing, nil
}

func (a *Agent) hasRepo(r string) bool {
	for _, have := range a.Repos {
		if normRepo(have) == normRepo(r) {
			return true
		}
	}
	return false
}

func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(perm); err != nil && !isUnsupported(err) {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Package config loads the agentsign tool configuration: where the agent store
// lives and how the calling agent is detected.
//
// The config file is shared by every agent on a host. It is located, in order:
//
//  1. the --config flag
//  2. $AGENTSIGN_CONFIG
//  3. <user config dir>/agentsign/config.toml
//  4. /etc/agentsign/config.toml (non-Windows)
//
// A missing file is not an error: defaults apply.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
)

// EnvConfig names the environment variable that points at the config file.
const EnvConfig = "AGENTSIGN_CONFIG"

// EnvAgent is the variable agentsign always checks, ahead of any configured ones.
const EnvAgent = "AGENTSIGN_AGENT"

// AgentPlaceholder is substituted with the agent id in Store and path rules.
const AgentPlaceholder = "{agent}"

// Config is the on-disk config.toml.
type Config struct {
	// Store is the agent store root. Each agent lives in <Store>/<agent-id>/.
	// It may contain {agent}, for platforms that give each agent its own
	// private folder (e.g. "/data/workspaces/{agent}/.agentsign").
	Store string `toml:"store"`

	// Env lists extra environment variables that name the agent, checked in
	// order after AGENTSIGN_AGENT.
	Env []string `toml:"env,omitempty"`

	// PathRules map a repo location to an agent, e.g. "/workspaces/{agent}/".
	PathRules []string `toml:"path_rules,omitempty"`

	// SSHKeygen is the real ssh-keygen binary. Defaults to "ssh-keygen" on PATH.
	SSHKeygen string `toml:"ssh_keygen,omitempty"`

	// Path is where this config was loaded from ("" when defaults are in use).
	Path string `toml:"-"`
}

// DefaultPath returns the per-user config location.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agentsign", "config.toml"), nil
}

// DefaultStore returns the per-user agent store location.
func DefaultStore() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agentsign", "agents"), nil
}

// candidates lists config paths in lookup order, given an explicit path.
func candidates(explicit string) []string {
	if explicit != "" {
		return []string{explicit}
	}
	if p := os.Getenv(EnvConfig); p != "" {
		return []string{p}
	}
	var out []string
	if p, err := DefaultPath(); err == nil {
		out = append(out, p)
	}
	if runtime.GOOS != "windows" {
		out = append(out, "/etc/agentsign/config.toml")
	}
	return out
}

// Load finds and parses the config. An explicitly named file (flag or
// environment) must exist; implicit locations fall back to defaults.
func Load(explicit string) (*Config, error) {
	paths := candidates(explicit)
	mustExist := explicit != "" || os.Getenv(EnvConfig) != ""
	for _, p := range paths {
		c, err := parseFile(p)
		if err == nil {
			return c, nil
		}
		if errors.Is(err, fs.ErrNotExist) && !mustExist {
			continue
		}
		return nil, err
	}
	return withDefaults(&Config{})
}

func parseFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	md, err := toml.Decode(string(data), &c)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if un := md.Undecoded(); len(un) > 0 {
		return nil, fmt.Errorf("config %s: unknown key %q", path, un[0].String())
	}
	c.Path = path
	return withDefaults(&c)
}

func withDefaults(c *Config) (*Config, error) {
	if c.Store == "" {
		s, err := DefaultStore()
		if err != nil {
			return nil, err
		}
		c.Store = s
	}
	if c.SSHKeygen == "" {
		c.SSHKeygen = "ssh-keygen"
	}
	for _, r := range c.PathRules {
		if strings.Count(r, AgentPlaceholder) != 1 {
			return nil, fmt.Errorf("path rule %q must contain %s exactly once", r, AgentPlaceholder)
		}
	}
	return c, nil
}

// EnvVars returns the environment variables checked for the agent id, in order.
func (c *Config) EnvVars() []string {
	out := []string{EnvAgent}
	for _, v := range c.Env {
		if v != "" && v != EnvAgent {
			out = append(out, v)
		}
	}
	return out
}

// StoreFor returns the store root for an agent, expanding {agent}.
func (c *Config) StoreFor(agentID string) string {
	return strings.ReplaceAll(c.Store, AgentPlaceholder, agentID)
}

// Encode renders the config as TOML.
func (c *Config) Encode() (string, error) {
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(c); err != nil {
		return "", err
	}
	return b.String(), nil
}

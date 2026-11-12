package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/KIFFCollective/agentsign/internal/resolve"
	"github.com/KIFFCollective/agentsign/internal/store"
)

// EnvRealGH points nested gh calls straight at the real binary.
const EnvRealGH = "AGENTSIGN_REAL_GH"

// isGHShim reports whether agentsign was invoked under the name gh (a
// symlink /usr/local/bin/gh -> agentsign ahead of the real gh on PATH).
func isGHShim(argv0 string) bool {
	b := strings.ToLower(filepath.Base(argv0))
	return b == "gh" || b == "gh.exe"
}

// cmdGHShim runs the real GitHub CLI with the agent's own token in GH_TOKEN,
// for this process only. Without it, agents log gh in with their token,
// which gh stores in ~/.config/gh/hosts.yml: on hosts where agents share a
// home, every agent can then read it, and gh uses it with no repo limit.
//
// With no agent identified, gh runs unchanged.
func (a *app) cmdGHShim(args []string) error {
	configured := ""
	if c, err := a.config(); err == nil {
		configured = c.GH
	}
	ghBin, err := a.realBinary("gh", EnvRealGH, configured)
	if err != nil {
		return err
	}
	env := append(os.Environ(), EnvRealGH+"="+ghBin)

	_, ag, err := a.currentAgent()
	switch {
	case errors.Is(err, resolve.ErrNoAgent):
		return a.runReal(ghBin, args, env)
	case err != nil:
		a.warnf("gh: %v; running gh without the agent's token", err)
		return a.runReal(ghBin, args, env)
	}

	if sub := blockedGHAuth(args); sub != "" {
		return fmt.Errorf("gh auth %s is disabled for agents: agentsign already gives gh agent %s's token for each call, "+
			"and a stored token would be readable by anything sharing this home", sub, ag.ID)
	}
	if ag.TokenFile == "" {
		if err := store.CheckPerm(fileIn(ag.Dir, store.TokenFile)); err != nil {
			return err
		}
	}
	tok, err := ag.Token()
	if err != nil {
		return fmt.Errorf("gh: agent %s has no usable token (%v); %s", ag.ID, err, doctorHint)
	}
	// The agent's token replaces any token in the environment.
	env = withoutEnv(env, "GH_TOKEN", "GITHUB_TOKEN")
	env = append(env, "GH_TOKEN="+tok, "AGENTSIGN_AGENT="+ag.ID)
	return a.runReal(ghBin, args, env)
}

// blockedGHAuth returns the gh auth subcommand in args if it would store,
// switch or configure credentials, or "".
func blockedGHAuth(args []string) string {
	var pos []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
		}
	}
	// gh has no global flags before the command, so "auth" comes first.
	// Flag values may sit between it and the subcommand
	// (gh auth --hostname x login), so look at every later word.
	if len(pos) < 2 || pos[0] != "auth" {
		return ""
	}
	for _, p := range pos[1:] {
		switch p {
		case "login", "logout", "refresh", "setup-git", "switch":
			return p
		}
	}
	return ""
}

func withoutEnv(env []string, keys ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, key := range keys {
			if strings.EqualFold(k, key) {
				drop = true
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// ghHostsFile is where gh stores credentials when it can't use a keyring.
func (a *app) ghHostsFile() string {
	if d := a.getenv("GH_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "hosts.yml")
	}
	if d := a.getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "gh", "hosts.yml")
	}
	if runtime.GOOS == "windows" {
		if d := a.getenv("AppData"); d != "" {
			return filepath.Join(d, "GitHub CLI", "hosts.yml")
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "gh", "hosts.yml")
	}
	return ""
}

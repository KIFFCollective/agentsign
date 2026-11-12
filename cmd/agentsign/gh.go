package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	// The agent's token replaces any token in the environment; when the
	// call isn't allowed one, gh runs with none.
	env = withoutEnv(env, "GH_TOKEN", "GITHUB_TOKEN")
	env = append(env, "AGENTSIGN_AGENT="+ag.ID)

	// gh api has no -R; agents may still pass one to say which repo the
	// call is for. It's used for scoping below, then removed.
	runArgs := args
	if len(args) > 0 && args[0] == "api" {
		runArgs = stripRepoFlag(args)
	}

	// Apply the agent's repo list, as the credential helper does for git.
	if len(ag.Repos) > 0 {
		targets, unknown := a.ghTargets(args)
		if unknown != "" {
			a.warnf("gh: no token given to this call: %s. (Agent %s is limited to %s.)",
				unknown, ag.ID, strings.Join(ag.Repos, ", "))
			return a.runReal(ghBin, runArgs, env)
		}
		for _, t := range targets {
			if t.host != "github.com" {
				a.warnf("gh: no token given for host %s (agentsign handles github.com)", t.host)
				return a.runReal(ghBin, runArgs, env)
			}
			if !ag.AllowsRepo(t.repo) {
				return fmt.Errorf("gh: agent %s is not allowed to access %s (from %s); allowed: %s",
					ag.ID, t.repo, t.source, strings.Join(ag.Repos, ", "))
			}
		}
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
	return a.runReal(ghBin, runArgs, append(env, "GH_TOKEN="+tok))
}

// ghTarget is a repository a gh call may act on.
type ghTarget struct {
	host, repo string // "github.com", "owner/name"
	source     string // where it came from, for messages
}

// ghTargets works out which repositories a gh call may act on, in the order
// gh itself uses: -R/--repo, $GH_REPO, repos named in the arguments, then the
// checkout's remotes. If it can't tell, it returns why instead.
func (a *app) ghTargets(args []string) ([]ghTarget, string) {
	if v, ok := ghRepoFlag(args); ok {
		t, err := parseGHRepo(v)
		if err != nil {
			return nil, err.Error()
		}
		t.source = "-R " + v
		return []ghTarget{t}, ""
	}
	if v := a.getenv("GH_REPO"); v != "" {
		t, err := parseGHRepo(v)
		if err != nil {
			return nil, "GH_REPO: " + err.Error()
		}
		t.source = "GH_REPO"
		return []ghTarget{t}, ""
	}

	pos := ghPositionals(args)
	var named []ghTarget
	for _, p := range pos {
		if isGitHubURL(p) {
			if t, err := parseGHRepo(p); err == nil {
				t.source = p
				named = append(named, t)
			}
		}
	}
	// gh repo clone|view|fork|... OWNER/REPO
	if len(pos) >= 3 && pos[0] == "repo" && strings.Count(pos[2], "/") >= 1 && !isGitHubURL(pos[2]) {
		if t, err := parseGHRepo(pos[2]); err == nil {
			t.source = "gh repo " + pos[1] + " " + pos[2]
			named = append(named, t)
		}
	}
	isAPI := len(args) > 0 && args[0] == "api"
	if isAPI {
		ep, fields := ghAPIArgs(args[1:])
		parts := strings.Split(strings.TrimPrefix(ep, "/"), "/")
		switch {
		case len(parts) >= 3 && parts[0] == "repos" && !strings.HasPrefix(parts[1], "{"):
			named = append(named, ghTarget{host: "github.com", repo: parts[1] + "/" + parts[2], source: "gh api " + ep})
		case ep == "graphql" && fields["owner"] != "" && fields["name"] != "" &&
			!strings.HasPrefix(fields["owner"], "{") && !strings.HasPrefix(fields["name"], "{"):
			named = append(named, ghTarget{host: "github.com", repo: fields["owner"] + "/" + fields["name"], source: "gh api graphql owner/name fields"})
		}
		// Anything else ({owner}/{repo} placeholders, graphql, user, ...)
		// falls back to the checkout below, as gh pr does.
	}
	if len(named) > 0 {
		return named, ""
	}

	remotes := a.checkoutRemotes()
	if len(remotes) == 0 {
		if isAPI {
			return nil, "can't tell which repository this gh api call is for: run it inside a checkout of an allowed repo, " +
				"use a repos/OWNER/REPO/... endpoint, or add -R OWNER/REPO (agentsign removes it before running gh)"
		}
		return nil, "not inside a checkout with a GitHub remote; pass -R OWNER/REPO"
	}
	return remotes, ""
}

// ghAPIValueFlags are gh api flags that take a value.
var ghAPIValueFlags = map[string]bool{
	"-X": true, "--method": true, "-H": true, "--header": true,
	"-f": true, "--raw-field": true, "-F": true, "--field": true,
	"--input": true, "-q": true, "--jq": true, "-t": true, "--template": true,
	"--hostname": true, "--cache": true, "-p": true, "--preview": true,
	"-R": true, "--repo": true,
}

// ghAPIArgs returns the endpoint of a gh api call (its first argument that
// isn't a flag or a flag's value) and its -f/-F fields.
func ghAPIArgs(args []string) (endpoint string, fields map[string]string) {
	fields = map[string]string{}
	field := func(kv string) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			fields[k] = v
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			if endpoint == "" && i+1 < len(args) {
				endpoint = args[i+1]
			}
			return endpoint, fields
		case ghAPIValueFlags[a] && i+1 < len(args):
			if a == "-f" || a == "-F" || a == "--field" || a == "--raw-field" {
				field(args[i+1])
			}
			i++
		case strings.HasPrefix(a, "--") && strings.Contains(a, "="):
			name, val, _ := strings.Cut(a, "=")
			if name == "--field" || name == "--raw-field" {
				field(val)
			}
		case len(a) > 2 && (strings.HasPrefix(a, "-f") || strings.HasPrefix(a, "-F")):
			field(a[2:])
		case strings.HasPrefix(a, "-"):
		case endpoint == "":
			endpoint = a
		}
	}
	return endpoint, fields
}

// stripRepoFlag removes -R/--repo from args. gh api doesn't accept it, but
// agents may pass it to tell agentsign which repo the call is for.
func stripRepoFlag(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(out, args[i:]...)
		case (a == "-R" || a == "--repo") && i+1 < len(args):
			i++
		case strings.HasPrefix(a, "--repo="), strings.HasPrefix(a, "-R") && len(a) > 2:
		default:
			out = append(out, a)
		}
	}
	return out
}

// checkoutRemotes returns the GitHub repos of the current checkout's
// remotes: only the `gh repo set-default` one if set, else all of them
// (gh may pick any, e.g. upstream over origin).
func (a *app) checkoutRemotes() []ghTarget {
	gitBin, err := a.realGit()
	if err != nil {
		return nil
	}
	out, err := exec.Command(gitBin, "config", "--get-regexp", `^remote\..*\.(url|gh-resolved)$`).Output()
	if err != nil {
		return nil
	}
	urls := map[string]string{}
	base := ""
	var order []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, val, _ := strings.Cut(line, " ")
		name := strings.TrimPrefix(key, "remote.")
		switch {
		case strings.HasSuffix(name, ".url"):
			name = strings.TrimSuffix(name, ".url")
			if _, seen := urls[name]; !seen {
				order = append(order, name)
			}
			urls[name] = val
		case strings.HasSuffix(name, ".gh-resolved") && val == "base":
			base = strings.TrimSuffix(name, ".gh-resolved")
		}
	}
	if base != "" {
		order = []string{base}
	}
	var out2 []ghTarget
	for _, name := range order {
		u, ok := urls[name]
		if !ok || !isGitHubRemote(u) {
			continue
		}
		if t, err := parseGHRepo(u); err == nil {
			t.source = "remote " + name
			out2 = append(out2, t)
		}
	}
	return out2
}

// ghRepoFlag finds -R/--repo in gh arguments.
func ghRepoFlag(args []string) (string, bool) {
	for i, a := range args {
		switch {
		case a == "--":
			return "", false
		case (a == "-R" || a == "--repo") && i+1 < len(args):
			return args[i+1], true
		case strings.HasPrefix(a, "--repo="):
			return strings.TrimPrefix(a, "--repo="), true
		case strings.HasPrefix(a, "-R") && len(a) > 2:
			return strings.TrimPrefix(strings.TrimPrefix(a, "-R"), "="), true
		}
	}
	return "", false
}

// ghPositionals returns arguments that aren't flags. Flag values are
// included; callers only look for repo-shaped words and known positions.
func ghPositionals(args []string) []string {
	var pos []string
	for _, a := range args {
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
		}
	}
	return pos
}

func isGitHubURL(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

func isGitHubRemote(u string) bool {
	return strings.Contains(u, "://") || strings.Contains(u, "@")
}

// parseGHRepo parses the repository forms gh accepts: OWNER/REPO,
// HOST/OWNER/REPO, https://HOST/OWNER/REPO[/...], git@HOST:OWNER/REPO and
// ssh://git@HOST/OWNER/REPO, with or without .git.
func parseGHRepo(s string) (ghTarget, error) {
	orig := s
	host := "github.com"
	switch {
	case strings.Contains(s, "://"):
		_, rest, _ := strings.Cut(s, "://")
		if at := strings.IndexByte(rest, '@'); at >= 0 && at < strings.IndexByte(rest+"/", '/') {
			rest = rest[at+1:]
		}
		h, path, _ := strings.Cut(rest, "/")
		host, s = h, path
	case strings.Contains(s, "@") && strings.Contains(s, ":"):
		_, rest, _ := strings.Cut(s, "@")
		h, path, _ := strings.Cut(rest, ":")
		host, s = h, path
	case strings.Count(s, "/") == 2:
		h, path, _ := strings.Cut(s, "/")
		host, s = h, path
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ghTarget{}, fmt.Errorf("can't read a repository from %q", orig)
	}
	host = strings.ToLower(strings.TrimSuffix(host, ":443"))
	if host == "www.github.com" {
		host = "github.com"
	}
	return ghTarget{host: host, repo: parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")}, nil
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

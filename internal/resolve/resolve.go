// Package resolve works out which agent is calling.
//
// Order: explicit flag, then environment variables (AGENTSIGN_AGENT first,
// then any configured), then path rules matched against the working directory.
// No match is a normal outcome: callers must then refuse to act, never fall
// back to another identity.
package resolve

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/KIFFCollective/agentsign/internal/config"
)

// ErrNoAgent means no agent could be identified.
var ErrNoAgent = errors.New("no agent identified")

// Source records how the agent was found.
type Source string

const (
	SourceFlag Source = "flag"
	SourceEnv  Source = "env"
	SourcePath Source = "path"
)

// Result is a resolved agent.
type Result struct {
	ID     string
	Source Source
	Detail string // env var name or matched path rule
}

// Input is everything resolution depends on, so it can be tested without
// touching the process environment.
type Input struct {
	Flag   string
	Getenv func(string) string
	Cwd    string
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// ValidID reports whether id is a safe agent id (also used as a folder name).
func ValidID(id string) bool {
	return validID.MatchString(id) && id != "." && id != ".."
}

// Resolve identifies the agent.
func Resolve(c *config.Config, in Input) (*Result, error) {
	if in.Flag != "" {
		return checked(in.Flag, SourceFlag, "--agent")
	}
	if in.Getenv != nil {
		for _, name := range c.EnvVars() {
			if v := strings.TrimSpace(in.Getenv(name)); v != "" {
				return checked(v, SourceEnv, name)
			}
		}
	}
	if in.Cwd != "" {
		realCwd := canonical(in.Cwd)
		for _, rule := range c.PathRules {
			if id, ok := MatchPath(rule, in.Cwd); ok {
				return checked(id, SourcePath, rule)
			}
			// git runs helpers from the resolved worktree path, which can be
			// spelled differently from the rule: symlinks (macOS /var ->
			// /private/var) or Windows 8.3 short names (RUNNER~1). Compare
			// real paths too.
			if id, ok := MatchPath(canonicalRule(rule), realCwd); ok {
				return checked(id, SourcePath, rule)
			}
		}
	}
	return nil, ErrNoAgent
}

// canonical resolves symlinks (and, on Windows, short names and case) in
// p. Paths that don't exist are returned cleaned but otherwise unchanged.
func canonical(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		if abs, err := filepath.Abs(r); err == nil {
			return abs
		}
		return r
	}
	return filepath.Clean(p)
}

// canonicalRule resolves the fixed part of a rule (before {agent}).
func canonicalRule(rule string) string {
	before, after, ok := strings.Cut(rule, config.AgentPlaceholder)
	if !ok || before == "" {
		return rule
	}
	return filepath.ToSlash(canonical(before)) + "/" + config.AgentPlaceholder + after
}

func checked(id string, src Source, detail string) (*Result, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid agent id %q from %s %s", id, src, detail)
	}
	return &Result{ID: id, Source: src, Detail: detail}, nil
}

// MatchPath matches a path rule such as "/workspaces/{agent}/" against dir.
// The rule's prefix must match whole path segments, and {agent} matches
// exactly one segment. Matching is on slash-normalised, cleaned paths.
func MatchPath(rule, dir string) (string, bool) {
	before, after, ok := strings.Cut(norm(rule), config.AgentPlaceholder)
	if !ok {
		return "", false
	}
	d := norm(dir)
	if !strings.HasSuffix(d, "/") {
		d += "/"
	}
	if !strings.HasPrefix(d, before) || (before != "" && !strings.HasSuffix(before, "/")) {
		return "", false
	}
	rest := d[len(before):]
	seg, tail, found := strings.Cut(rest, "/")
	if !found || seg == "" {
		return "", false
	}
	// Anything in the rule after {agent} must follow the segment.
	after = strings.TrimPrefix(after, "/")
	if after != "" && !strings.HasPrefix(tail, after) {
		return "", false
	}
	return seg, true
}

func norm(p string) string {
	trailing := strings.HasSuffix(p, "/") || strings.HasSuffix(p, `\`)
	c := filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
	if trailing && !strings.HasSuffix(c, "/") {
		c += "/"
	}
	return c
}

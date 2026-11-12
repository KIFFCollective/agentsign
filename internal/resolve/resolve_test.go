package resolve

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/KIFFCollective/agentsign/internal/config"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveOrder(t *testing.T) {
	c := &config.Config{Env: []string{"OMB_BOT_ID"}, PathRules: []string{"/workspaces/{agent}/"}}
	cases := []struct {
		name   string
		in     Input
		want   string
		source Source
	}{
		{"flag wins", Input{Flag: "a", Getenv: env(map[string]string{"AGENTSIGN_AGENT": "b"}), Cwd: "/workspaces/c/repo"}, "a", SourceFlag},
		{"own env before configured", Input{Getenv: env(map[string]string{"AGENTSIGN_AGENT": "b", "OMB_BOT_ID": "x"})}, "b", SourceEnv},
		{"configured env", Input{Getenv: env(map[string]string{"OMB_BOT_ID": "x"}), Cwd: "/workspaces/c"}, "x", SourceEnv},
		{"path", Input{Getenv: env(nil), Cwd: "/workspaces/c/repo/sub"}, "c", SourcePath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Resolve(c, tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if r.ID != tc.want || r.Source != tc.source {
				t.Fatalf("got %s/%s, want %s/%s", r.ID, r.Source, tc.want, tc.source)
			}
		})
	}
}

func TestResolveNone(t *testing.T) {
	c := &config.Config{PathRules: []string{"/workspaces/{agent}/"}}
	_, err := Resolve(c, Input{Getenv: env(nil), Cwd: "/home/human/repo"})
	if !errors.Is(err, ErrNoAgent) {
		t.Fatalf("want ErrNoAgent, got %v", err)
	}
}

func TestResolveRejectsBadID(t *testing.T) {
	c := &config.Config{}
	for _, id := range []string{"../b", "a/b", ".hidden", "", "a b"} {
		if id == "" {
			continue
		}
		if _, err := Resolve(c, Input{Flag: id}); err == nil {
			t.Errorf("id %q accepted", id)
		}
	}
}

// A rule written with a symlinked path must match the real path git uses.
func TestResolvePathThroughSymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "dev", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("can't create symlinks here: %v", err)
	}
	cases := []struct{ rule, cwd string }{
		{filepath.ToSlash(link) + "/{agent}/", filepath.Join(real, "dev", "repo")}, // rule via link, cwd real
		{filepath.ToSlash(real) + "/{agent}/", filepath.Join(link, "dev", "repo")}, // rule real, cwd via link
	}
	for _, tc := range cases {
		c := &config.Config{PathRules: []string{tc.rule}}
		r, err := Resolve(c, Input{Getenv: env(nil), Cwd: tc.cwd})
		if err != nil || r.ID != "dev" {
			t.Errorf("rule %s, cwd %s: got %+v, %v", tc.rule, tc.cwd, r, err)
		}
	}
}

func TestMatchPath(t *testing.T) {
	cases := []struct {
		rule, dir, want string
		ok              bool
	}{
		{"/workspaces/{agent}/", "/workspaces/dev", "dev", true},
		{"/workspaces/{agent}/", "/workspaces/dev/repo", "dev", true},
		{"/workspaces/{agent}/", "/workspaces/", "", false},
		{"/workspaces/{agent}/", "/workspacesX/dev", "", false},
		{"/workspaces/{agent}/", "/other/workspaces/dev", "", false},
		{"/data/{agent}/repos/", "/data/dev/repos/x", "dev", true},
		{"/data/{agent}/repos/", "/data/dev/other", "", false},
		{"/workspaces/{agent}/", "/workspaces/a/../b/repo", "b", true},
	}
	for _, tc := range cases {
		got, ok := MatchPath(tc.rule, tc.dir)
		if got != tc.want || ok != tc.ok {
			t.Errorf("MatchPath(%q, %q) = %q,%v want %q,%v", tc.rule, tc.dir, got, ok, tc.want, tc.ok)
		}
	}
}

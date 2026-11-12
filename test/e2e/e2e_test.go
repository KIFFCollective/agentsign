// Package e2e runs agentsign against real git and ssh-keygen in temp folders.
// No network is used. Skipped with -short or when git/ssh-keygen are missing.
package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type env struct {
	t    *testing.T
	bin  string
	root string
	base []string
}

func setup(t *testing.T) *env {
	t.Helper()
	if testing.Short() {
		t.Skip("e2e skipped with -short")
	}
	for _, tool := range []string{"git", "ssh-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin", "agentsign")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "../../cmd/agentsign")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	e := &env{t: t, bin: bin, root: root}
	// Isolate from the developer's own git and agentsign config.
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "GIT_"), strings.HasPrefix(k, "AGENTSIGN_"), k == "HOME", k == "XDG_CONFIG_HOME",
			k == "SSH_ASKPASS", k == "DISPLAY":
			continue
		}
		e.base = append(e.base, kv)
	}
	e.base = append(e.base,
		"HOME="+root,
		"XDG_CONFIG_HOME="+filepath.Join(root, "xdg"),
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		// Never pop up a password dialog (Git for Windows sets SSH_ASKPASS);
		// git ignores empty values and then can't prompt at all.
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		// Never reach the real GitHub API from tests; tests that need it
		// start a fake server and override this.
		"GITHUB_API_URL=http://127.0.0.1:1",
		"AGENTSIGN_CONFIG="+filepath.Join(root, "config.toml"),
	)
	return e
}

// run executes name with args in dir and extra env, returning stdout+stderr.
func (e *env) run(dir string, extra []string, stdin string, name string, args ...string) (string, error) {
	e.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(append([]string{}, e.base...), extra...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func (e *env) must(dir string, extra []string, stdin, name string, args ...string) string {
	e.t.Helper()
	out, err := e.run(dir, extra, stdin, name, args...)
	if err != nil {
		e.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

func (e *env) agentsign(dir string, extra []string, stdin string, args ...string) string {
	e.t.Helper()
	return e.must(dir, extra, stdin, e.bin, args...)
}

func TestEndToEnd(t *testing.T) {
	e := setup(t)
	ws := filepath.Join(e.root, "workspaces")
	rule := filepath.ToSlash(ws) + "/{agent}/"

	e.agentsign(e.root, nil, "", "init", "--global", "--store", filepath.Join(e.root, "agents"), "--path-rule", rule, "--env", "OMB_BOT_ID", "--exe", e.bin)
	e.agentsign(e.root, nil, "", "agent", "add", "alice", "--name", "Alice", "--email", "alice@bots.test", "--repo", "acme/app")
	e.agentsign(e.root, nil, "", "agent", "add", "bob", "--name", "Bob", "--email", "bob@bots.test")
	if out, err := e.run(e.root, nil, "", e.bin, "agent", "add", "carol", "--email", "alice@bots.test"); err == nil {
		t.Fatalf("duplicate email accepted: %s", out)
	}
	e.agentsign(e.root, nil, "github_pat_alice\n", "agent", "token", "alice", "--expires", "2099-01-01")

	repo := filepath.Join(ws, "bob", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	e.must(repo, nil, "", "git", "init", "-q", "-b", "main")

	alice := []string{"AGENTSIGN_AGENT=alice"}
	// Identity via the git wrapper.
	e.agentsign(repo, alice, "", "git", "commit", "-q", "--allow-empty", "-m", "base")
	base := strings.TrimSpace(e.must(repo, nil, "", "git", "rev-parse", "HEAD"))
	e.agentsign(repo, alice, "", "git", "commit", "-q", "--allow-empty", "-m", "by alice")

	// Identity via `agentsign env` variables; agent found by configured env var.
	bobEnv := []string{"OMB_BOT_ID=bob",
		"GIT_AUTHOR_NAME=Bob", "GIT_AUTHOR_EMAIL=bob@bots.test",
		"GIT_COMMITTER_NAME=Bob", "GIT_COMMITTER_EMAIL=bob@bots.test"}
	e.must(repo, bobEnv, "", "git", "commit", "-q", "--allow-empty", "-m", "by bob (env)")

	// Agent found by path rule (repo is under workspaces/bob/).
	bobPath := bobEnv[1:]
	e.must(repo, bobPath, "", "git", "commit", "-q", "--allow-empty", "-m", "by bob (path)")

	t.Run("signer refuses another agent's email", func(t *testing.T) {
		forged := append([]string{"AGENTSIGN_AGENT=alice"}, bobEnv[1:]...)
		out, err := e.run(repo, forged, "", "git", "commit", "-q", "--allow-empty", "-m", "forged")
		if err == nil || !strings.Contains(out, "identity mismatch") {
			t.Fatalf("forged commit was signed: %v\n%s", err, out)
		}
	})

	t.Run("signer refuses with no agent", func(t *testing.T) {
		other := filepath.Join(e.root, "elsewhere")
		e.must(e.root, nil, "", "git", "init", "-q", other)
		human := []string{"GIT_AUTHOR_NAME=H", "GIT_AUTHOR_EMAIL=h@example.com", "GIT_COMMITTER_NAME=H", "GIT_COMMITTER_EMAIL=h@example.com"}
		out, err := e.run(other, human, "", "git", "commit", "-q", "--allow-empty", "-m", "x")
		if err == nil || !strings.Contains(out, "no agent identified") {
			t.Fatalf("commit with no agent was signed: %v\n%s", err, out)
		}
	})

	t.Run("git refuses commit without identity", func(t *testing.T) {
		out, err := e.run(repo, alice, "", "git", "commit", "-q", "--allow-empty", "-m", "x")
		if err == nil {
			t.Fatalf("commit without identity succeeded (user.useConfigOnly not applied)\n%s", out)
		}
	})

	// Bypass the wrapper: sign with alice's key directly, but as bob.
	aliceKey := filepath.Join(e.root, "agents", "alice", "signing_key")
	e.must(repo, bobEnv, "", "git",
		"-c", "gpg.ssh.program=ssh-keygen", "-c", "user.signingKey="+aliceKey,
		"commit", "-q", "--allow-empty", "-m", "forged, bypassing agentsign")

	// Unsigned commit claiming to be alice.
	aliceIdent := []string{"GIT_AUTHOR_NAME=A", "GIT_AUTHOR_EMAIL=alice@bots.test", "GIT_COMMITTER_NAME=A", "GIT_COMMITTER_EMAIL=alice@bots.test"}
	e.must(repo, aliceIdent, "", "git", "-c", "commit.gpgSign=false", "commit", "-q", "--allow-empty", "-m", "unsigned alice")

	signersPath := filepath.Join(repo, ".github", "allowed_signers")
	e.agentsign(repo, nil, "", "signers", "--out", signersPath)
	e.agentsign(repo, nil, "", "--agent", "alice", "doctor", "--signers", signersPath)

	out, err := e.run(repo, nil, "", e.bin, "verify", "--signers", signersPath, "--format", "json", base+"..HEAD")
	if err == nil {
		t.Fatalf("verify passed a range with forged commits:\n%s", out)
	}
	for _, want := range []string{
		`"subject": "by alice"`,
		`signed with alice@bots.test's key but committer is bob@bots.test`,
		`no valid signature from a registered agent key`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("verify output missing %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, `"status": "ok"`); n != 3 {
		t.Errorf("want 3 ok commits, got %d:\n%s", n, out)
	}
	if n := strings.Count(out, `"status": "fail"`); n != 2 {
		t.Errorf("want 2 failed commits, got %d:\n%s", n, out)
	}

	// The good part of the range alone passes.
	good := strings.TrimSpace(e.must(repo, nil, "", "git", "rev-parse", "HEAD~2"))
	e.agentsign(repo, nil, "", "verify", "--signers", signersPath, base+".."+good)

	t.Run("credential helper", func(t *testing.T) {
		fill := func(extra []string, path string) (string, error) {
			return e.run(repo, extra, "protocol=https\nhost=github.com\npath="+path+"\n\n", "git", "credential", "fill")
		}
		out, err := fill(alice, "acme/app.git")
		if err != nil || !strings.Contains(out, "password=github_pat_alice") {
			t.Fatalf("alice/acme/app: %v\n%s", err, out)
		}
		if out, _ := fill(alice, "acme/other.git"); strings.Contains(out, "password=github_pat") {
			t.Fatalf("token sent for a repo outside alice's list:\n%s", out)
		}
		// Add a wildcard, then remove it again.
		e.agentsign(e.root, nil, "", "agent", "repo", "add", "alice", "acme/*")
		if out, err := fill(alice, "acme/other.git"); err != nil || !strings.Contains(out, "password=github_pat_alice") {
			t.Fatalf("acme/* not honoured: %v\n%s", err, out)
		}
		e.agentsign(e.root, nil, "", "agent", "repo", "remove", "alice", "acme/*")
		if out, _ := fill(alice, "acme/other.git"); strings.Contains(out, "password=github_pat") {
			t.Fatalf("token still sent after removing acme/*:\n%s", out)
		}
		// Removing the last repo would mean "all repos", so it's refused.
		if out, err := e.run(e.root, nil, "", e.bin, "agent", "repo", "remove", "alice", "acme/app"); err == nil || !strings.Contains(out, "allow-all") {
			t.Fatalf("removing the last repo was not refused: %v\n%s", err, out)
		}
		if out := e.agentsign(e.root, nil, "", "agent", "repo", "list", "alice"); strings.TrimSpace(out) != "acme/app" {
			t.Fatalf("repo list after refused remove: %q", out)
		}
		if out, _ := fill([]string{"AGENTSIGN_AGENT=bob"}, "acme/app.git"); strings.Contains(out, "password=github_pat") {
			t.Fatalf("bob has no token but got one:\n%s", out)
		}
		if out, _ := fill(nil, "acme/app.git"); strings.Contains(out, "password=github_pat") {
			t.Fatalf("credentials returned with no agent:\n%s", out)
		}
	})

	t.Run("revoke", func(t *testing.T) {
		e.agentsign(e.root, nil, "", "agent", "revoke", "bob", "--yes")
		e.agentsign(repo, nil, "", "signers", "--out", signersPath)
		data, _ := os.ReadFile(signersPath)
		if !strings.Contains(string(data), "# agentsign-revoked: bob@bots.test") {
			t.Fatalf("revoked marker missing:\n%s", data)
		}
		out, err := e.run(repo, nil, "", e.bin, "verify", "--signers", signersPath, base+".."+good)
		if err == nil || !strings.Contains(out, "revoked agent") {
			t.Fatalf("revoked agent's commits passed:\n%s", out)
		}
	})
}

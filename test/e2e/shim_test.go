package e2e

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// With agentsign installed as "git" ahead of the real git, a plain
// `git commit` gets the agent's identity and Agent trailer; with no agent,
// git runs unchanged.
func TestGitShim(t *testing.T) {
	e := setup(t)
	e.agentsign(e.root, nil, "", "init", "--global", "--store", filepath.Join(e.root, "agents"), "--exe", e.bin)
	e.agentsign(e.root, nil, "", "agent", "add", "pip", "--name", "Pip", "--email", "pip@troupe.test")

	// Install the shim as a copy (symlinks need privileges on Windows).
	shimDir := filepath.Join(e.root, "shim")
	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	copyFile(t, e.bin, filepath.Join(shimDir, name))
	path := []string{"PATH=" + shimDir + string(os.PathListSeparator) + os.Getenv("PATH")}
	// exec.Command resolves "git" on the test's own PATH, so call the shim by path.
	git := filepath.Join(shimDir, name)

	repo := filepath.Join(e.root, "repo")
	// No agent: plain passthrough (init works, no identity needed).
	e.must(e.root, path, "", git, "init", "-q", "-b", "main", repo)
	if out := e.must(repo, path, "", git, "--version"); !strings.HasPrefix(out, "git version") {
		t.Fatalf("passthrough --version: %q", out)
	}

	// With an agent: no GIT_* identity variables, no wrapper, just git.
	pip := append([]string{"AGENTSIGN_AGENT=pip"}, path...)
	e.must(repo, pip, "", git, "commit", "-q", "--allow-empty", "-m", "plain git commit")
	got := e.must(repo, nil, "", "git", "log", "-1", "--format=%an <%ae>|%cn <%ce>|%(trailers:key=Agent,valueonly)")
	want := "Pip <pip@troupe.test>|Pip <pip@troupe.test>|Pip <pip@troupe.test>"
	if !strings.HasPrefix(strings.TrimSpace(got), want) {
		t.Fatalf("commit through shim = %q, want prefix %q", got, want)
	}

	// The commit verifies, trailer included.
	signers := filepath.Join(repo, "allowed_signers")
	e.agentsign(repo, nil, "", "signers", "--out", signers)
	e.agentsign(repo, nil, "", "verify", "--signers", signers, "--require-trailer", "HEAD")

	// Without an agent, commits are not given an identity or trailer:
	// user.useConfigOnly makes git refuse, as without the shim.
	if out, err := e.run(repo, path, "", git, "commit", "-q", "--allow-empty", "-m", "no agent"); err == nil {
		t.Fatalf("commit without agent succeeded through shim:\n%s", out)
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

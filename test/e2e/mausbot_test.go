package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The MausBot setup: agents found by path rules (no env var), git and gh
// shims, and the platform's own commits with an explicit identity left alone.
func TestMausBotStyle(t *testing.T) {
	e := setup(t)
	data := filepath.Join(e.root, "data")
	rules := []string{
		filepath.ToSlash(filepath.Join(data, "task-workspaces")) + "/{agent}/",
		filepath.ToSlash(filepath.Join(data, "workspaces")) + "/{agent}/",
	}
	store := filepath.ToSlash(filepath.Join(data, "workspaces")) + "/{agent}/.agentsign"
	e.agentsign(e.root, nil, "", "init", "--global", "--store", store, "--path-rule", rules[0], "--path-rule", rules[1], "--exe", e.bin)
	e.agentsign(e.root, nil, "", "agent", "add", "bot-pip", "--name", "Pip", "--email", "pip@troupe.test")
	e.agentsign(e.root, nil, "tok-pip\n", "agent", "token", "bot-pip")

	// Shims: agentsign installed as git and gh (copies; symlinks need
	// privileges on Windows). A fake gh stands in for the real one.
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	shim := filepath.Join(e.root, "shim")
	if err := os.MkdirAll(shim, 0o755); err != nil {
		t.Fatal(err)
	}
	git, gh := filepath.Join(shim, "git"+ext), filepath.Join(shim, "gh"+ext)
	copyFile(t, e.bin, git)
	copyFile(t, e.bin, gh)
	fakegh := filepath.Join(e.root, "fakegh"+ext)
	if out, err := exec.Command("go", "build", "-o", fakegh, "./testdata/fakegh").CombinedOutput(); err != nil {
		t.Fatalf("build fakegh: %v\n%s", err, out)
	}
	env := []string{"PATH=" + shim + string(os.PathListSeparator) + os.Getenv("PATH"), "AGENTSIGN_REAL_GH=" + fakegh}

	// A conversation folder, as MausBot creates them. No AGENTSIGN_AGENT or
	// OMB_BOT_ID anywhere: the folder identifies the bot.
	task := filepath.Join(data, "task-workspaces", "bot-pip", "thread-1")
	e.must(e.root, env, "", git, "init", "-q", "-b", "main", task)
	e.must(task, env, "", git, "commit", "-q", "--allow-empty", "-m", "from a task folder")
	got := e.must(task, nil, "", "git", "log", "-1", "--format=%an <%ae>|%(trailers:key=Agent,valueonly)")
	if !strings.HasPrefix(strings.TrimSpace(got), "Pip <pip@troupe.test>|Pip <pip@troupe.test>") {
		t.Fatalf("path-rule commit = %q", got)
	}

	// The platform's own commit with an explicit identity: left alone.
	checkpoint := []string{"GIT_AUTHOR_NAME=OpenMausBot Checkpoint", "GIT_AUTHOR_EMAIL=checkpoint@mausbot.local",
		"GIT_COMMITTER_NAME=OpenMausBot Checkpoint", "GIT_COMMITTER_EMAIL=checkpoint@mausbot.local"}
	e.must(task, append(env, checkpoint...), "", git, "-c", "commit.gpgSign=false", "commit", "-q", "--allow-empty", "-m", "checkpoint")
	got = e.must(task, nil, "", "git", "log", "-1", "--format=%an <%ae>|%(trailers:key=Agent,valueonly)|")
	if strings.TrimSpace(got) != "OpenMausBot Checkpoint <checkpoint@mausbot.local>||" {
		t.Fatalf("checkpoint commit was rewritten: %q", got)
	}

	// gh: the agent's token, only for this process, replacing any in the env.
	out := e.must(task, append(env, "GH_TOKEN=someone-elses", "GITHUB_TOKEN=ci"), "", gh, "pr", "create", "--fill")
	if !strings.Contains(out, "fakegh GH_TOKEN=tok-pip GITHUB_TOKEN= args=pr create --fill") {
		t.Fatalf("gh shim: %q", out)
	}
	// ...and agents can't store a token.
	if out, err := e.run(task, env, "", gh, "auth", "login", "--with-token"); err == nil || !strings.Contains(out, "gh auth login is disabled for agents") {
		t.Fatalf("gh auth login not blocked: %v\n%s", err, out)
	}
	// Outside the bot folders gh runs unchanged.
	out = e.must(e.root, append(env, "GH_TOKEN=operator"), "", gh, "auth", "status")
	if !strings.Contains(out, "fakegh GH_TOKEN=operator ") {
		t.Fatalf("gh passthrough: %q", out)
	}

	// Outside the bot folders nobody is identified, and signing refuses
	// with a pointer to doctor.
	elsewhere := filepath.Join(e.root, "tmp-clone")
	e.must(e.root, env, "", git, "init", "-q", elsewhere)
	human := []string{"GIT_AUTHOR_NAME=X", "GIT_AUTHOR_EMAIL=x@example.com", "GIT_COMMITTER_NAME=X", "GIT_COMMITTER_EMAIL=x@example.com"}
	out, err := e.run(elsewhere, append(env, human...), "", git, "commit", "-q", "--allow-empty", "-m", "x")
	if err == nil || !strings.Contains(out, "agentsign doctor") {
		t.Fatalf("commit outside bot folders: %v\n%s", err, out)
	}

	// doctor flags a token stored by gh auth login.
	cfg := filepath.Join(e.root, "ghconfig")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "hosts.yml"), []byte("github.com:\n    oauth_token: gho_leaked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ = e.run(task, []string{"GH_CONFIG_DIR=" + cfg}, "", e.bin, "doctor", "--offline")
	if !strings.Contains(out, "holds a stored token") || !strings.Contains(out, "agent          bot-pip (from path") {
		t.Fatalf("doctor:\n%s", out)
	}
}

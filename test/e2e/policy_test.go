package e2e

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// init must not take over a person's git config by accident.
func TestInitGuardsHumanConfig(t *testing.T) {
	e := setup(t)
	human := filepath.Join(e.root, "human-gitconfig")
	if err := os.WriteFile(human, []byte("[user]\n\temail = person@example.com\n[credential]\n\thelper = manager\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	asHuman := []string{"GIT_CONFIG_GLOBAL=" + human}
	store := filepath.Join(e.root, "agents")

	out, err := e.run(e.root, asHuman, "", e.bin, "init", "--store", store, "--exe", e.bin)
	if err == nil || !strings.Contains(out, "choose one of --global, --system or --file") {
		t.Fatalf("init without a scope: %v\n%s", err, out)
	}

	out, err = e.run(e.root, asHuman, "", e.bin, "init", "--global", "--store", store, "--exe", e.bin)
	if err == nil || !strings.Contains(out, "user.email = person@example.com") || !strings.Contains(out, "credential.helper = manager") || !strings.Contains(out, "--agent-host") {
		t.Fatalf("init --global on a human config was not refused: %v\n%s", err, out)
	}
	if data, _ := os.ReadFile(human); strings.Contains(string(data), "agentsign") {
		t.Fatalf("refused init still changed the config:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(e.root, "config.toml")); err == nil {
		t.Fatal("refused init still wrote config.toml")
	}

	// --file leaves the human config alone.
	agentCfg := filepath.Join(e.root, "agent-gitconfig")
	out = e.agentsign(e.root, asHuman, "", "init", "--file", agentCfg, "--store", store, "--exe", e.bin)
	if !strings.Contains(out, "GIT_CONFIG_GLOBAL=") {
		t.Errorf("--file output doesn't explain GIT_CONFIG_GLOBAL:\n%s", out)
	}
	if data, _ := os.ReadFile(human); strings.Contains(string(data), "agentsign") {
		t.Fatalf("--file changed the human config:\n%s", data)
	}

	// --agent-host overrides the check.
	e.agentsign(e.root, asHuman, "", "init", "--global", "--agent-host", "--force", "--store", store, "--exe", e.bin)

	// Re-running on a config agentsign wrote itself passes without --agent-host.
	e.agentsign(e.root, []string{"GIT_CONFIG_GLOBAL=" + agentCfg}, "", "init", "--global", "--force", "--store", store, "--exe", e.bin)
}

func TestHumansGitHubVerified(t *testing.T) {
	e := setup(t)
	e.agentsign(e.root, nil, "", "init", "--global", "--store", filepath.Join(e.root, "agents"), "--exe", e.bin)
	e.agentsign(e.root, nil, "", "agent", "add", "alice", "--email", "alice@bots.test")

	repo := filepath.Join(e.root, "repo")
	e.must(e.root, nil, "", "git", "init", "-q", "-b", "main", repo)
	human := []string{"GIT_AUTHOR_NAME=H", "GIT_AUTHOR_EMAIL=h@example.com", "GIT_COMMITTER_NAME=H", "GIT_COMMITTER_EMAIL=h@example.com"}
	unsigned := []string{"-c", "commit.gpgSign=false", "commit", "-q", "--allow-empty", "-m"}
	e.must(repo, human, "", "git", append(unsigned, "verified on github")...)
	verified := strings.TrimSpace(e.must(repo, nil, "", "git", "rev-parse", "HEAD"))
	e.must(repo, human, "", "git", append(unsigned, "not verified")...)
	// A made-up agent email: GitHub would call it Verified (it's on the
	// machine account), so only --agent-domain catches it.
	ghost := []string{"GIT_AUTHOR_NAME=G", "GIT_AUTHOR_EMAIL=ghost@bots.test", "GIT_COMMITTER_NAME=G", "GIT_COMMITTER_EMAIL=ghost@bots.test"}
	e.must(repo, ghost, "", "git", append(unsigned, "ghost agent")...)
	ghostSHA := strings.TrimSpace(e.must(repo, nil, "", "git", "rev-parse", "HEAD"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/repos/acme/app/commits/" + verified, "/repos/acme/app/commits/" + ghostSHA:
			w.Write([]byte(`{"commit":{"verification":{"verified":true,"reason":"valid"}}}`))
		default:
			w.Write([]byte(`{"commit":{"verification":{"verified":false,"reason":"unsigned"}}}`))
		}
	}))
	defer srv.Close()

	signers := filepath.Join(repo, "allowed_signers")
	e.agentsign(repo, nil, "", "signers", "--out", signers)
	gh := []string{"GITHUB_API_URL=" + srv.URL, "GITHUB_TOKEN=test-token", "GITHUB_REPOSITORY=acme/app"}

	out, err := e.run(repo, gh, "", e.bin, "verify", "--signers", signers, "--humans", "github-verified",
		"--agent-domain", "bots.test", "--format", "json", "HEAD")
	if err == nil {
		t.Fatalf("verify passed:\n%s", out)
	}
	for _, want := range []string{
		`"status": "human"`,
		`human commit is not Verified on GitHub (unsigned)`,
		`ghost@bots.test is on agent domain bots.test but is not a registered agent`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, `"status": "human"`); n != 1 {
		t.Errorf("want 1 accepted human commit, got %d:\n%s", n, out)
	}

	// Without --agent-domain the ghost passes as a human; verify warns.
	out, _ = e.run(repo, gh, "", e.bin, "verify", "--signers", signers, "--humans", "github-verified", verified)
	if !strings.Contains(out, "set --agent-domain") {
		t.Errorf("no --agent-domain warning:\n%s", out)
	}

	// API failures fail closed.
	bad := []string{"GITHUB_API_URL=" + srv.URL, "GITHUB_TOKEN=wrong", "GITHUB_REPOSITORY=acme/app"}
	out, err = e.run(repo, bad, "", e.bin, "verify", "--signers", signers, "--humans", "github-verified", "--agent-domain", "bots.test", verified)
	if err == nil || !strings.Contains(out, "could not check GitHub verification") {
		t.Fatalf("API failure didn't fail closed: %v\n%s", err, out)
	}
}

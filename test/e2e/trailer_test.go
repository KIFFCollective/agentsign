package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAgentTrailerAndStatuses(t *testing.T) {
	e := setup(t)
	e.agentsign(e.root, nil, "", "init", "--global", "--store", filepath.Join(e.root, "agents"), "--exe", e.bin)
	e.agentsign(e.root, nil, "", "agent", "add", "pip", "--name", "Pip", "--email", "pip@troupe.test")
	e.agentsign(e.root, nil, "", "agent", "add", "odin", "--name", "Odin", "--email", "odin@troupe.test")

	repo := filepath.Join(e.root, "repo")
	e.must(e.root, nil, "", "git", "init", "-q", "-b", "main", repo)
	head := func() string { return strings.TrimSpace(e.must(repo, nil, "", "git", "rev-parse", "HEAD")) }
	message := func() string { return e.must(repo, nil, "", "git", "log", "-1", "--format=%B") }

	e.must(repo, []string{"GIT_AUTHOR_NAME=H", "GIT_AUTHOR_EMAIL=h@example.com", "GIT_COMMITTER_NAME=H", "GIT_COMMITTER_EMAIL=h@example.com"},
		"", "git", "-c", "commit.gpgSign=false", "commit", "-q", "--allow-empty", "-m", "base")
	base := head()

	if got := strings.TrimSpace(e.agentsign(repo, nil, "", "--agent", "pip", "trailer")); got != "Agent: Pip <pip@troupe.test>" {
		t.Fatalf("trailer = %q", got)
	}

	// The git wrapper adds the trailer, and --amend doesn't duplicate it.
	e.agentsign(repo, nil, "", "--agent", "pip", "git", "commit", "-q", "--allow-empty", "-m", "by pip")
	e.agentsign(repo, nil, "", "--agent", "pip", "git", "commit", "-q", "--amend", "--no-edit", "--allow-empty")
	if msg := message(); strings.Count(msg, "Agent: Pip <pip@troupe.test>") != 1 {
		t.Fatalf("want exactly one trailer, message:\n%s", msg)
	}
	pipSHA := head()

	odinEnv := []string{"AGENTSIGN_AGENT=odin", "GIT_AUTHOR_NAME=Odin", "GIT_AUTHOR_EMAIL=odin@troupe.test",
		"GIT_COMMITTER_NAME=Odin", "GIT_COMMITTER_EMAIL=odin@troupe.test"}
	// Plain git commit (no wrapper): no trailer, still a valid agent commit.
	e.must(repo, odinEnv, "", "git", "commit", "-q", "--allow-empty", "-m", "by odin, no trailer")
	noTrailer := head()
	// Signed by odin, but the trailer claims Pip.
	e.must(repo, odinEnv, "", "git", "commit", "-q", "--allow-empty", "-m", "lying trailer\n\nAgent: Pip <pip@troupe.test>")
	lying := head()
	// A human commit claiming to be an agent.
	e.must(repo, []string{"GIT_AUTHOR_NAME=H", "GIT_AUTHOR_EMAIL=h@example.com", "GIT_COMMITTER_NAME=H", "GIT_COMMITTER_EMAIL=h@example.com"},
		"", "git", "-c", "commit.gpgSign=false", "commit", "-q", "--allow-empty", "-m", "human claims agent\n\nAgent: Odin <odin@troupe.test>")
	human := head()

	signers := filepath.Join(repo, "allowed_signers")
	e.agentsign(repo, nil, "", "signers", "--out", signers)

	// Capture statuses posted to a fake GitHub API.
	var mu sync.Mutex
	statuses := map[string]map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sha := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/repos/acme/app/statuses/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		statuses[sha] = body
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	gh := []string{"GITHUB_API_URL=" + srv.URL, "GITHUB_TOKEN=t", "GITHUB_REPOSITORY=acme/app",
		"GITHUB_SERVER_URL=https://github.example", "GITHUB_RUN_ID=42"}

	out, err := e.run(repo, gh, "", e.bin, "verify", "--signers", signers, "--post-statuses", "--format", "json", base+"..HEAD")
	if err == nil {
		t.Fatalf("verify passed:\n%s", out)
	}
	for _, want := range []string{
		`"agent_trailers": [` + "\n" + `        "Pip \u003cpip@troupe.test\u003e"`,
		`Agent trailer says \"Pip \u003cpip@troupe.test\u003e\" but the commit is signed by Odin \u003codin@troupe.test\u003e`,
		`Agent: Odin \u003codin@troupe.test\u003e trailer on a commit not signed by a registered agent`,
		"Posted 4 of 4 commit statuses (agentsign/signer)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}

	check := func(sha, state, desc string) {
		t.Helper()
		st, ok := statuses[sha]
		if !ok {
			t.Errorf("no status for %.7s", sha)
			return
		}
		if st["state"] != state || !strings.Contains(st["description"], desc) || st["context"] != "agentsign/signer" ||
			st["target_url"] != "https://github.example/acme/app/actions/runs/42" {
			t.Errorf("status for %.7s = %v, want %s containing %q", sha, st, state, desc)
		}
	}
	check(pipSHA, "success", "Signed by Pip <pip@troupe.test> · key SHA256:")
	check(noTrailer, "success", "Signed by Odin <odin@troupe.test>")
	check(lying, "failure", "Agent trailer says")
	check(human, "failure", "trailer on a commit not signed")

	// --require-trailer rejects agent commits without one.
	out, err = e.run(repo, nil, "", e.bin, "verify", "--signers", signers, "--require-trailer", base+".."+noTrailer)
	if err == nil || !strings.Contains(out, `missing Agent trailer (want "Agent: Odin <odin@troupe.test>")`) {
		t.Fatalf("--require-trailer: %v\n%s", err, out)
	}
	// ...and accepts the wrapper's commits.
	e.agentsign(repo, nil, "", "verify", "--signers", signers, "--require-trailer", base+".."+pipSHA)
}

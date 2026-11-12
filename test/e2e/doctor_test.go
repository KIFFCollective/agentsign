package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// doctor's GitHub checks against a fake API: token owner and expiry, and
// whether each agent's key is registered as a signing key.
func TestDoctorGitHubChecks(t *testing.T) {
	e := setup(t)
	e.agentsign(e.root, nil, "", "init", "--global", "--store", filepath.Join(e.root, "agents"), "--exe", e.bin)
	for _, a := range []string{"good", "authkey", "missing", "expiring"} {
		e.agentsign(e.root, nil, "", "agent", "add", a, "--email", a+"@bots.test", "--username", "bots")
		e.agentsign(e.root, nil, "tok-"+a+"\n", "agent", "token", a)
	}
	pub := func(id string) string {
		out := e.agentsign(e.root, nil, "", "agent", "show", id)
		for _, l := range strings.Split(out, "\n") {
			if v, ok := strings.CutPrefix(l, "public:"); ok {
				return strings.TrimSpace(v)
			}
		}
		t.Fatalf("no public key for %s", id)
		return ""
	}
	keys := func(ids ...string) []byte {
		var out []map[string]string
		for _, id := range ids {
			out = append(out, map[string]string{"key": strings.Join(strings.Fields(pub(id))[:2], " ")})
		}
		b, _ := json.Marshal(out)
		return b
	}
	signing, auth := keys("good", "expiring"), keys("authkey")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			switch r.Header.Get("Authorization") {
			case "Bearer tok-expiring":
				w.Header().Set("GitHub-Authentication-Token-Expiration", "2000-01-01 00:00:00 UTC")
			case "Bearer tok-good", "Bearer tok-authkey", "Bearer tok-missing":
			default:
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"message":"Bad credentials"}`))
				return
			}
			w.Write([]byte(`{"login":"bots"}`))
		case "/users/bots/ssh_signing_keys":
			w.Write(signing)
		case "/users/bots/keys":
			w.Write(auth)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	api := []string{"GITHUB_API_URL=" + srv.URL}

	doctor := func(agent string, extra ...string) (string, error) {
		args := append([]string{"--agent", agent, "doctor"}, extra...)
		return e.run(e.root, api, "", e.bin, args...)
	}
	expect := func(agent string, wantFail bool, wants ...string) {
		t.Helper()
		out, err := doctor(agent)
		if (err != nil) != wantFail {
			t.Errorf("%s: failed=%v, want %v\n%s", agent, err != nil, wantFail, out)
		}
		for _, w := range wants {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %q\n%s", agent, w, out)
			}
		}
	}
	expect("good", false, "accepted; belongs to bots", "registered on bots as a signing key", "none (GitHub reports no expiry)")
	expect("authkey", true, "AUTHENTICATION key, not a signing key")
	expect("missing", true, "not on bots; add it as a signing key")
	expect("expiring", true, "expired 2000-01-01")

	// A revoked token.
	e.agentsign(e.root, nil, "tok-revoked\n", "agent", "token", "good")
	expect("good", true, "rejected by GitHub (Bad credentials)")

	// --offline skips the API entirely.
	out, err := doctor("missing", "--offline")
	if err != nil || strings.Contains(out, "github") {
		t.Errorf("--offline still checked GitHub: %v\n%s", err, out)
	}
}

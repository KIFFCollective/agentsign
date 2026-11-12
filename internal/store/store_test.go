package store

import (
	"strings"
	"testing"
)

func TestCreateLoadToken(t *testing.T) {
	s := New(t.TempDir())
	a := &Agent{ID: "dev", Name: "Dev", Email: "dev@bots.example", Repos: []string{"acme/app"}}
	if err := s.Create(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(&Agent{ID: "dev", Email: "x@y"}); err == nil {
		t.Fatal("duplicate create succeeded")
	}
	got, err := s.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != a.Email || got.Hosts[0] != "github.com" {
		t.Fatalf("unexpected agent %+v", got)
	}
	pub, err := got.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") || !strings.HasSuffix(pub, " dev@bots.example") {
		t.Fatalf("public key %q", pub)
	}
	if err := CheckPerm(got.KeyPath()); err != nil {
		t.Fatal(err)
	}

	if _, err := got.Token(); err == nil {
		t.Fatal("token read before set")
	}
	if err := s.SetToken(got, "bad token", nil); err == nil {
		t.Fatal("token with space accepted")
	}
	if err := s.SetToken(got, "  github_pat_abc\n", nil); err != nil {
		t.Fatal(err)
	}
	tok, err := got.Token()
	if err != nil || tok != "github_pat_abc" {
		t.Fatalf("token %q, %v", tok, err)
	}
}

func TestRotateKeepsOld(t *testing.T) {
	s := New(t.TempDir())
	a := &Agent{ID: "dev", Email: "dev@bots.example"}
	if err := s.Create(a); err != nil {
		t.Fatal(err)
	}
	before, _ := a.Fingerprint()
	if err := s.GenerateKey(a); err != nil {
		t.Fatal(err)
	}
	after, _ := a.Fingerprint()
	if before == after {
		t.Fatal("rotate kept the same key")
	}
}

func TestRevoke(t *testing.T) {
	s := New(t.TempDir())
	a := &Agent{ID: "dev", Email: "dev@bots.example"}
	if err := s.Create(a); err != nil {
		t.Fatal(err)
	}
	_ = s.SetToken(a, "tok", nil)
	if err := s.Revoke(a); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("dev")
	if err != nil || !got.Revoked {
		t.Fatalf("revoked agent %+v, %v", got, err)
	}
	if _, err := got.PublicKey(); err == nil {
		t.Fatal("key still present")
	}
	if _, err := got.Token(); err == nil {
		t.Fatal("token still present")
	}
}

func TestAllows(t *testing.T) {
	a := &Agent{Hosts: []string{"github.com"}, Repos: []string{"Acme/App"}}
	if !a.AllowsHost("GitHub.com") || a.AllowsHost("gitlab.com") {
		t.Fatal("host check")
	}
	for _, r := range []string{"acme/app", "acme/app.git", "/acme/app/"} {
		if !a.AllowsRepo(r) {
			t.Errorf("repo %q denied", r)
		}
	}
	if a.AllowsRepo("acme/other") {
		t.Fatal("other repo allowed")
	}
	if !(&Agent{}).AllowsRepo("any/thing") {
		t.Fatal("empty repo list should allow")
	}
}

func TestWildcard(t *testing.T) {
	a := &Agent{Repos: []string{"acme/*", "other/one"}}
	for _, r := range []string{"acme/app", "ACME/api.git", "other/one"} {
		if !a.AllowsRepo(r) {
			t.Errorf("repo %q denied", r)
		}
	}
	for _, r := range []string{"acmex/app", "other/two", "acme", "acme/"} {
		if a.AllowsRepo(r) {
			t.Errorf("repo %q allowed", r)
		}
	}
	if got := a.MatchingRepo("acme/app"); got != "acme/*" {
		t.Fatalf("MatchingRepo = %q", got)
	}
}

func TestCleanRepo(t *testing.T) {
	ok := map[string]string{"acme/app": "acme/app", "/Acme/App.git/": "Acme/App", "acme/*": "acme/*", " a.b/c_d-e ": "a.b/c_d-e"}
	for in, want := range ok {
		got, err := CleanRepo(in)
		if err != nil || got != want {
			t.Errorf("CleanRepo(%q) = %q, %v want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"acme", "*/*", "*", "acme/app/x", "acme/a*", "acme/", "/app", "ac me/app"} {
		if _, err := CleanRepo(in); err == nil {
			t.Errorf("CleanRepo(%q) accepted", in)
		}
	}
}

func TestAddRemoveRepos(t *testing.T) {
	a := &Agent{Repos: []string{"acme/app"}}
	added, err := a.AddRepos("ACME/app", "acme/api.git", "acme/api")
	if err != nil || len(added) != 1 || added[0] != "acme/api" {
		t.Fatalf("added %v, %v", added, err)
	}
	if _, err := a.AddRepos("bad"); err == nil {
		t.Fatal("bad repo accepted")
	}
	removed, missing, err := a.RemoveRepos("Acme/App", "acme/nope")
	if err != nil || len(removed) != 1 || removed[0] != "acme/app" || len(missing) != 1 || missing[0] != "acme/nope" {
		t.Fatalf("removed %v missing %v err %v", removed, missing, err)
	}
	if len(a.Repos) != 1 || a.Repos[0] != "acme/api" {
		t.Fatalf("repos %v", a.Repos)
	}
}

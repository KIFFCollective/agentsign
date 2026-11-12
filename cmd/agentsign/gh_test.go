package main

import (
	"strings"
	"testing"
)

func TestBlockedGHAuth(t *testing.T) {
	cases := map[string]string{
		"auth login --with-token":    "login",
		"auth setup-git":             "setup-git",
		"auth --hostname x login":    "login",
		"auth status":                "",
		"auth token":                 "",
		"pr create --title login":    "",
		"api /user":                  "",
		"auth refresh -s workflow":   "refresh",
		"auth switch --user someone": "switch",
	}
	for in, want := range cases {
		if got := blockedGHAuth(strings.Fields(in)); got != want {
			t.Errorf("blockedGHAuth(%q) = %q want %q", in, got, want)
		}
	}
}

func TestParseGHRepo(t *testing.T) {
	ok := map[string]string{
		"acme/app":                                 "github.com acme/app",
		"github.com/acme/app":                      "github.com acme/app",
		"ghe.example/acme/app":                     "ghe.example acme/app",
		"https://github.com/acme/app":              "github.com acme/app",
		"https://github.com/acme/app.git":          "github.com acme/app",
		"https://github.com/acme/app/pull/12":      "github.com acme/app",
		"https://x-access-token@github.com/a/b":    "github.com a/b",
		"git@github.com:acme/app.git":              "github.com acme/app",
		"ssh://git@github.com/acme/app.git":        "github.com acme/app",
		"https://www.github.com/acme/app/issues/1": "github.com acme/app",
	}
	for in, want := range ok {
		got, err := parseGHRepo(in)
		if err != nil || got.host+" "+got.repo != want {
			t.Errorf("parseGHRepo(%q) = %+v, %v want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"acme", "https://github.com/acme", ""} {
		if _, err := parseGHRepo(in); err == nil {
			t.Errorf("parseGHRepo(%q) accepted", in)
		}
	}
}

func TestGHTargets(t *testing.T) {
	a := &app{getenv: func(k string) string {
		if k == "PATH" {
			return "" // no git: checkout lookup finds nothing
		}
		return ""
	}, cfg: nil}
	cases := []struct {
		args, want, unknown string
	}{
		{"pr list -R acme/other", "acme/other", ""},
		{"pr list --repo=acme/other", "acme/other", ""},
		{"pr list -Racme/other", "acme/other", ""},
		{"pr view https://github.com/acme/other/pull/1", "acme/other", ""},
		{"repo clone acme/other", "acme/other", ""},
		{"api repos/acme/other/pulls", "acme/other", ""},
		{"api /repos/acme/other/pulls --paginate", "acme/other", ""},
		{"api graphql -f query=x", "", "isn't scoped"},
		{"api user", "", "isn't scoped"},
		{"api repos/{owner}/{repo}/pulls", "", "not inside a checkout"},
		{"pr list", "", "not inside a checkout"},
	}
	for _, tc := range cases {
		targets, unknown := a.ghTargets(strings.Fields(tc.args))
		var got []string
		for _, tg := range targets {
			got = append(got, tg.repo)
		}
		if strings.Join(got, ",") != tc.want || (tc.unknown == "") != (unknown == "") || !strings.Contains(unknown, tc.unknown) {
			t.Errorf("ghTargets(%q) = %v, %q want %q, %q", tc.args, got, unknown, tc.want, tc.unknown)
		}
	}
}

func TestWithoutEnv(t *testing.T) {
	got := withoutEnv([]string{"A=1", "GH_TOKEN=x", "github_token=y", "B=2"}, "GH_TOKEN", "GITHUB_TOKEN")
	if strings.Join(got, ",") != "A=1,B=2" {
		t.Fatalf("got %v", got)
	}
}

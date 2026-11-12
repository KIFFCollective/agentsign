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

func TestWithoutEnv(t *testing.T) {
	got := withoutEnv([]string{"A=1", "GH_TOKEN=x", "github_token=y", "B=2"}, "GH_TOKEN", "GITHUB_TOKEN")
	if strings.Join(got, ",") != "A=1,B=2" {
		t.Fatalf("got %v", got)
	}
}

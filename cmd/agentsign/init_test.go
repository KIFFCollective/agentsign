package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestInjectTrailer(t *testing.T) {
	const tr = "Agent: Pip <pip@x>"
	cases := map[string]string{
		"commit -m hi":                      "commit --trailer " + tr + " -m hi",
		"-C repo -c a=b commit --amend":     "-C repo -c a=b commit --trailer " + tr + " --amend",
		"--no-pager commit":                 "--no-pager commit --trailer " + tr,
		"push origin main":                  "push origin main",
		"-c commit.gpgSign=true log commit": "-c commit.gpgSign=true log commit",
	}
	for in, want := range cases {
		got := injectTrailer(strings.Fields(in), tr)
		// The trailer is one argument; compare after re-splitting it.
		if !reflect.DeepEqual(strings.Fields(strings.Join(got, " ")), strings.Fields(want)) {
			t.Errorf("injectTrailer(%q) = %q want %q", in, got, want)
		}
	}
}

func TestHelperCommand(t *testing.T) {
	cases := map[string]string{
		"/usr/local/bin/agentsign":                      "/usr/local/bin/agentsign credential",
		"C:/tools/agentsign.exe":                        "C:/tools/agentsign.exe credential",
		"C:/Program Files/agentsign/agentsign.exe":      "!'C:/Program Files/agentsign/agentsign.exe' credential",
		"C:/Users/RUNNER~1/AppData/Local/agentsign.exe": "!'C:/Users/RUNNER~1/AppData/Local/agentsign.exe' credential",
	}
	for in, want := range cases {
		if got := helperCommand(in); got != want {
			t.Errorf("helperCommand(%q) = %q want %q", in, got, want)
		}
	}
}

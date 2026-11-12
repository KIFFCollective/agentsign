package main

import "testing"

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

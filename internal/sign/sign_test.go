package sign

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := map[string]Op{
		"-Y sign -n git -f k -U buf":      OpSign,
		"-Y verify -n git -f a -I p -s s": OpPassthrough,
		"-Y find-principals -f a -s s":    OpPassthrough,
		"-Y check-novalidate -n git -s s": OpPassthrough,
		"-l -f key":                       OpFingerprint,
		"-lf key":                         OpFingerprint,
		"-V":                              OpPassthrough,
	}
	for in, want := range cases {
		if got := Classify(strings.Fields(in)); got != want {
			t.Errorf("Classify(%q) = %s want %s", in, got, want)
		}
	}
}

func TestRewrite(t *testing.T) {
	cases := map[string]string{
		"-Y sign -n git -f agentsign -U /tmp/buf": "-Y sign -n git -f /k /tmp/buf",
		"-Y sign -n git -fagentsign /tmp/buf":     "-Y sign -n git -f /k /tmp/buf",
		"-Y sign -n git /tmp/buf":                 "-Y sign -n git -f /k /tmp/buf",
		"-lf agentsign":                           "-l -f /k",
	}
	for in, want := range cases {
		got := Rewrite(strings.Fields(in), "/k")
		if !reflect.DeepEqual(got, strings.Fields(want)) {
			t.Errorf("Rewrite(%q) = %q want %q", in, got, want)
		}
	}
}

const commit = `tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904
author Dev <dev@bots.example> 1700000000 +0000
committer Dev <Dev@Bots.Example> 1700000000 +0000

message mentioning committer Other <other@x>
`

func TestCheckIdentity(t *testing.T) {
	if err := CheckIdentity(strings.NewReader(commit), "dev@bots.example"); err != nil {
		t.Fatal(err)
	}
	err := CheckIdentity(strings.NewReader(commit), "other@x")
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("want mismatch, got %v", err)
	}
	tag := "object abc\ntype commit\ntag v1\ntagger Ops <ops@x> 1 +0000\n\nmsg\n"
	if err := CheckIdentity(strings.NewReader(tag), "dev@bots.example"); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("tag: want mismatch, got %v", err)
	}
	if err := CheckIdentity(strings.NewReader("arbitrary data\n"), "dev@bots.example"); err != nil {
		t.Fatalf("non-object payload: %v", err)
	}
}

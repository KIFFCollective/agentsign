package signers

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	a, _ := EntryFromPublicKey("b@x", "ssh-ed25519 AAAAB b@x", "b (Bee)")
	b, _ := EntryFromPublicKey("a@x", "ssh-ed25519 AAAAA", "a")
	f := &File{Entries: []Entry{a, b}, Revoked: []string{"old@x"}}
	out := Format(f)
	if !bytes.Contains(out, []byte("a@x namespaces=\"git\" ssh-ed25519 AAAAA\n")) {
		t.Fatalf("output:\n%s", out)
	}
	if bytes.Index(out, []byte("a@x")) > bytes.Index(out, []byte("b@x")) {
		t.Fatal("not sorted")
	}
	got, err := Parse(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(Format(got), out) {
		t.Fatalf("round trip differs:\n%s\nvs\n%s", Format(got), out)
	}
	emails := got.AgentEmails()
	if !reflect.DeepEqual(emails, map[string]bool{"a@x": true, "b@x": true, "old@x": true}) {
		t.Fatalf("emails %v", emails)
	}
	if !got.IsRevoked("OLD@x") || got.IsRevoked("a@x") {
		t.Fatal("revoked check")
	}
}

func TestParseVariants(t *testing.T) {
	in := `# comment
a@x,b@x ssh-ed25519 KEY1
c@x namespaces="git",valid-after="20240101" ssh-ed25519 KEY2
d@x cert-authority,namespaces="git file" ssh-ed25519 KEY3
`
	f, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 3 {
		t.Fatalf("entries %d", len(f.Entries))
	}
	if f.Entries[0].Options != "" || len(f.Entries[0].Principals) != 2 || f.Entries[0].Comment != "comment" {
		t.Fatalf("entry 0 %+v", f.Entries[0])
	}
	if f.Entries[2].Options != `cert-authority,namespaces="git file"` || f.Entries[2].Key != "KEY3" {
		t.Fatalf("entry 2 %+v", f.Entries[2])
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"a@x\n", "a@x opts notakey\n", `a@x namespaces="git ssh-ed25519 K` + "\n"} {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

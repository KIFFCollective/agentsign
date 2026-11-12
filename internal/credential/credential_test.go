package credential

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParseBasic(t *testing.T) {
	in := "protocol=https\nhost=github.com\npath=acme/app.git\ncapability[]=authtype\ncapability[]=state\n\nignored=after-blank\n"
	r, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if r.Protocol != "https" || r.Hostname() != "github.com" || r.Repo() != "acme/app" {
		t.Fatalf("got %+v", r)
	}
	if len(r.Other["capability[]"]) != 2 {
		t.Fatalf("unknown keys: %v", r.Other)
	}
	if _, ok := r.Other["ignored"]; ok {
		t.Fatal("read past blank line")
	}
}

func TestParseEOFAndCRLF(t *testing.T) {
	r, err := Parse(strings.NewReader("protocol=https\r\nhost=GitHub.com:443"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Protocol != "https" || r.Hostname() != "github.com" {
		t.Fatalf("got %+v", r)
	}
}

func TestParseURL(t *testing.T) {
	r, err := Parse(strings.NewReader("url=https://bot@github.com/acme/app.git\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Protocol != "https" || r.Host != "github.com" || r.Repo() != "acme/app" || r.Username != "bot" {
		t.Fatalf("got %+v", r)
	}
	// Explicit attributes after url= win.
	r, _ = Parse(strings.NewReader("url=https://github.com/acme/app\nhost=example.com\n"))
	if r.Host != "example.com" {
		t.Fatalf("host %q", r.Host)
	}
}

func TestParseMalformedRedacts(t *testing.T) {
	_, err := Parse(strings.NewReader("this-has-no-equals-and-is-long\n"))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "is-long") {
		t.Fatalf("error leaks input: %v", err)
	}
}

func TestRepo(t *testing.T) {
	for path, want := range map[string]string{
		"acme/app.git": "acme/app", "/acme/app": "acme/app", "acme": "", "": "", "acme/app/info/refs": "acme/app",
	} {
		if got := (&Request{Path: path}).Repo(); got != want {
			t.Errorf("Repo(%q) = %q want %q", path, got, want)
		}
	}
}

func TestWrite(t *testing.T) {
	exp := time.Unix(1700000000, 0)
	var b bytes.Buffer
	if err := (&Response{Username: "bot", Password: "tok", Expiry: &exp}).Write(&b); err != nil {
		t.Fatal(err)
	}
	want := "username=bot\npassword=tok\npassword_expiry_utc=1700000000\n"
	if b.String() != want {
		t.Fatalf("got %q", b.String())
	}
	if err := (&Response{Password: "a\nb"}).Write(&b); err == nil {
		t.Fatal("newline accepted")
	}
}

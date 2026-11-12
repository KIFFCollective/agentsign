package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

func TestCommitVerification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth header %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/repos/acme/app/commits/" + sha:
			w.Write([]byte(`{"commit":{"verification":{"verified":true,"reason":"valid"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"No commit found"}`))
		}
	}))
	defer srv.Close()
	env := map[string]string{"GITHUB_API_URL": srv.URL + "/", "GH_TOKEN": "tok"}
	c := NewFromEnv(func(k string) string { return env[k] })

	v, err := c.CommitVerification(context.Background(), "acme/app", sha)
	if err != nil || !v.Verified || v.Reason != "valid" {
		t.Fatalf("got %+v, %v", v, err)
	}
	_, err = c.CommitVerification(context.Background(), "acme/other", sha)
	if err == nil || !strings.Contains(err.Error(), "404: No commit found") {
		t.Fatalf("want 404 error, got %v", err)
	}
}

func TestCommitVerificationRejectsBadInput(t *testing.T) {
	c := &Client{BaseURL: "http://unused.invalid"}
	for _, tc := range [][2]string{{"acme", sha}, {"acme/app/../x", sha}, {"acme/app", "HEAD"}, {"acme/app", sha + "/../x"}} {
		if _, err := c.CommitVerification(context.Background(), tc[0], tc[1]); err == nil {
			t.Errorf("accepted %q %q", tc[0], tc[1])
		}
	}
}

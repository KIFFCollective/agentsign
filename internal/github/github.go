// Package github is a minimal GitHub REST client.
//
// Today it backs `agentsign verify`: the `github-verified` human policy
// (GET /repos/{owner}/{repo}/commits/{sha}) and per-commit statuses
// (POST /repos/{owner}/{repo}/statuses/{sha}). `agentsign github sync` (v0.2)
// will add uploading signing keys (POST /user/ssh_signing_keys) and emails
// (POST /user/emails).
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// DefaultAPI is the public GitHub API.
const DefaultAPI = "https://api.github.com"

// Client talks to the GitHub REST API.
type Client struct {
	BaseURL string // e.g. https://api.github.com or https://ghe.example/api/v3
	Token   string // optional; never logged
	HTTP    *http.Client
}

// NewFromEnv builds a client from GITHUB_API_URL and GITHUB_TOKEN (or
// GH_TOKEN), as set in GitHub Actions.
func NewFromEnv(getenv func(string) string) *Client {
	if getenv == nil {
		getenv = os.Getenv
	}
	base := getenv("GITHUB_API_URL")
	if base == "" {
		base = DefaultAPI
	}
	tok := getenv("GITHUB_TOKEN")
	if tok == "" {
		tok = getenv("GH_TOKEN")
	}
	return &Client{BaseURL: strings.TrimRight(base, "/"), Token: tok, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Verification is GitHub's verdict on a commit signature.
type Verification struct {
	Verified bool   `json:"verified"`
	Reason   string `json:"reason"` // e.g. "valid", "unsigned", "unknown_key"
}

var (
	repoRE = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
	shaRE  = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

func checkRepoSHA(repo, sha string) error {
	if !repoRE.MatchString(repo) {
		return fmt.Errorf("invalid repository %q (want owner/name)", repo)
	}
	if !shaRE.MatchString(sha) {
		return fmt.Errorf("invalid commit sha %q", sha)
	}
	return nil
}

// do sends a request and returns the body of a 2xx response.
func (c *Client) do(ctx context.Context, method, path string, payload any) ([]byte, error) {
	var reqBody io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "agentsign")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Message == "" {
			e.Message = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("GitHub API %d: %s", resp.StatusCode, e.Message)
	}
	return body, nil
}

// Status is a commit status (POST /repos/{owner}/{repo}/statuses/{sha}).
type Status struct {
	State       string `json:"state"` // success, failure, error or pending
	Context     string `json:"context"`
	Description string `json:"description"` // GitHub keeps at most 140 characters
	TargetURL   string `json:"target_url,omitempty"`
}

// MaxStatusDescription is GitHub's limit on a status description.
const MaxStatusDescription = 140

// CreateStatus sets a commit status. Needs statuses:write.
func (c *Client) CreateStatus(ctx context.Context, repo, sha string, s Status) error {
	if err := checkRepoSHA(repo, sha); err != nil {
		return err
	}
	if r := []rune(s.Description); len(r) > MaxStatusDescription {
		s.Description = string(r[:MaxStatusDescription-1]) + "…"
	}
	_, err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/statuses/"+sha, s)
	return err
}

// CommitVerification returns GitHub's signature verification for a commit.
func (c *Client) CommitVerification(ctx context.Context, repo, sha string) (*Verification, error) {
	if err := checkRepoSHA(repo, sha); err != nil {
		return nil, err
	}
	body, err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/commits/"+sha, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Commit struct {
			Verification *Verification `json:"verification"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("GitHub API: %w", err)
	}
	if out.Commit.Verification == nil {
		return nil, errors.New("GitHub API: response has no commit.verification")
	}
	return out.Commit.Verification, nil
}

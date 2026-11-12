// Package verify checks that each commit in a range by a registered agent is
// signed by that agent's own key. It backs both `agentsign verify` and the
// GitHub Action.
//
// GitHub only checks "key on account + email on account", so on a shared
// machine account agent A could sign a commit carrying agent B's email and
// still get "Verified". This package closes that gap: the principal that
// ssh-keygen finds for the signing key must equal the committer email.
package verify

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/KIFFCollective/agentsign/internal/signers"
)

// HumanPolicy decides what happens to commits whose committer is not an agent.
type HumanPolicy string

const (
	HumansAllow HumanPolicy = "allow" // accept (leave to other checks)
	HumansDeny  HumanPolicy = "deny"  // only agent commits allowed
)

// Options configure a verification run.
type Options struct {
	RepoDir     string // working directory for git; "" for current
	Range       string // e.g. "origin/main..HEAD"
	SignersFile string
	Humans      HumanPolicy
	// AgentDomains: committer emails at these domains must be registered
	// agents. Catches commits with made-up agent emails.
	AgentDomains []string
	// RequireAuthorMatch: agent commits must have author == committer.
	RequireAuthorMatch bool
	Git                string // git binary, default "git"
	SSHKeygen          string // ssh-keygen binary, default "ssh-keygen"
}

// Status of a single commit.
type Status string

const (
	StatusOK    Status = "ok"
	StatusFail  Status = "fail"
	StatusHuman Status = "human"
)

// Result is the outcome for one commit.
type Result struct {
	Commit      string `json:"commit"`
	Subject     string `json:"subject"`
	Committer   string `json:"committer"`
	Author      string `json:"author"`
	Agent       string `json:"agent,omitempty"` // principal found for the signing key
	Fingerprint string `json:"fingerprint,omitempty"`
	Status      Status `json:"status"`
	Reason      string `json:"reason,omitempty"`
}

// Report is the outcome for a range.
type Report struct {
	Results []Result `json:"results"`
}

// OK reports whether every commit passed.
func (r *Report) OK() bool {
	for _, res := range r.Results {
		if res.Status == StatusFail {
			return false
		}
	}
	return true
}

// Run verifies every commit in opts.Range.
func Run(ctx context.Context, opts Options) (*Report, error) {
	if opts.Git == "" {
		opts.Git = "git"
	}
	if opts.SSHKeygen == "" {
		opts.SSHKeygen = "ssh-keygen"
	}
	if opts.Humans == "" {
		opts.Humans = HumansAllow
	}
	if opts.Humans != HumansAllow && opts.Humans != HumansDeny {
		return nil, fmt.Errorf("unknown human policy %q", opts.Humans)
	}
	signersPath, err := filepath.Abs(opts.SignersFile)
	if err != nil {
		return nil, err
	}
	sf, err := readSigners(signersPath)
	if err != nil {
		return nil, err
	}
	agents := sf.AgentEmails()

	commits, err := listCommits(ctx, opts)
	if err != nil {
		return nil, err
	}
	rep := &Report{}
	for _, c := range commits {
		rep.Results = append(rep.Results, checkCommit(ctx, opts, signersPath, sf, agents, c))
	}
	return rep, nil
}

type commitInfo struct {
	sha, authorEmail, committerEmail, subject string
}

func listCommits(ctx context.Context, opts Options) ([]commitInfo, error) {
	// %x1f separates fields; subjects can't contain it.
	out, err := git(ctx, opts, "log", "--no-show-signature", "--format=%H%x1f%ae%x1f%ce%x1f%s", opts.Range, "--")
	if err != nil {
		return nil, err
	}
	var commits []commitInfo
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\x1f", 4)
		if len(f) != 4 {
			return nil, fmt.Errorf("unexpected git log line %q", line)
		}
		commits = append(commits, commitInfo{f[0], f[1], f[2], f[3]})
	}
	return commits, nil
}

var goodSig = regexp.MustCompile(`Good "git" signature for (\S+) with (\S+) key (SHA256:\S+)`)

func checkCommit(ctx context.Context, opts Options, signersPath string, sf *signers.File, agents map[string]bool, c commitInfo) Result {
	res := Result{Commit: c.sha, Subject: c.subject, Committer: c.committerEmail, Author: c.authorEmail}
	committer := strings.ToLower(c.committerEmail)

	if !agents[committer] {
		if d := domainOf(committer); d != "" && containsFold(opts.AgentDomains, d) {
			return fail(res, fmt.Sprintf("committer %s is on agent domain %s but is not a registered agent", c.committerEmail, d))
		}
		if opts.Humans == HumansDeny {
			return fail(res, "committer is not a registered agent and human commits are denied")
		}
		res.Status = StatusHuman
		return res
	}
	if sf.IsRevoked(committer) {
		return fail(res, "committer is a revoked agent")
	}
	if opts.RequireAuthorMatch && !strings.EqualFold(c.authorEmail, c.committerEmail) {
		return fail(res, fmt.Sprintf("author %s differs from agent committer", c.authorEmail))
	}

	raw, err := git(ctx, opts,
		"-c", "gpg.format=ssh",
		"-c", "gpg.ssh.program="+opts.SSHKeygen,
		"-c", "gpg.ssh.allowedSignersFile="+signersPath,
		"verify-commit", "--raw", c.sha)
	m := goodSig.FindStringSubmatch(raw)
	if err != nil || m == nil {
		return fail(res, "no valid signature from a registered agent key: "+summarise(raw, err))
	}
	res.Agent, res.Fingerprint = m[1], m[3]
	if !strings.EqualFold(res.Agent, c.committerEmail) {
		return fail(res, fmt.Sprintf("signed with %s's key but committer is %s", res.Agent, c.committerEmail))
	}
	res.Status = StatusOK
	return res
}

func fail(r Result, reason string) Result {
	r.Status, r.Reason = StatusFail, reason
	return r
}

func domainOf(email string) string {
	if i := strings.LastIndexByte(email, '@'); i >= 0 {
		return email[i+1:]
	}
	return ""
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimPrefix(v, "@"), s) {
			return true
		}
	}
	return false
}

func summarise(raw string, err error) string {
	s := strings.TrimSpace(raw)
	if s == "" && err != nil {
		s = err.Error()
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// git runs git and returns combined stdout+stderr (verify-commit --raw
// writes its status to stderr).
func git(ctx context.Context, opts Options, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, opts.Git, args...)
	cmd.Dir = opts.RepoDir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil && args[0] == "log" {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.String(), err
}

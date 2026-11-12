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
	// HumansGitHubVerified accepts a human commit only if GitHub marks it
	// Verified (this covers GitHub's own web-flow merge commits). Needs
	// Options.HumanCheck.
	HumansGitHubVerified HumanPolicy = "github-verified"
)

// HumanCheck decides whether a non-agent commit is acceptable, returning a
// short detail for the report (e.g. GitHub's verification reason).
type HumanCheck func(ctx context.Context, sha string) (ok bool, detail string, err error)

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
	// RequireTrailer: agent commits must carry an Agent: trailer.
	RequireTrailer bool
	// HumanCheck is used by the github-verified policy.
	HumanCheck HumanCheck
	Git        string // git binary, default "git"
	SSHKeygen  string // ssh-keygen binary, default "ssh-keygen"
}

// Status of a single commit.
type Status string

const (
	StatusOK    Status = "ok"
	StatusFail  Status = "fail"
	StatusHuman Status = "human"
)

// TrailerKey is the commit message trailer naming the agent:
//
//	Agent: Pip <pip@troupe.nexus>
//
// It makes commits self-describing on GitHub, where every agent on a shared
// machine account shows as that account. verify checks it against the
// signing key, so it can't claim someone else.
const TrailerKey = "Agent"

// Trailer formats the Agent trailer line for an agent.
func Trailer(name, email string) string {
	return fmt.Sprintf("%s: %s <%s>", TrailerKey, name, email)
}

// Result is the outcome for one commit.
type Result struct {
	Commit        string   `json:"commit"`
	Subject       string   `json:"subject"`
	Committer     string   `json:"committer"`
	CommitterName string   `json:"committer_name"`
	Author        string   `json:"author"`
	AgentTrailers []string `json:"agent_trailers,omitempty"` // values of Agent: trailers
	Agent         string   `json:"agent,omitempty"`          // principal found for the signing key
	Fingerprint   string   `json:"fingerprint,omitempty"`
	Status        Status   `json:"status"`
	Reason        string   `json:"reason,omitempty"`
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
	switch opts.Humans {
	case HumansAllow, HumansDeny:
	case HumansGitHubVerified:
		if opts.HumanCheck == nil {
			return nil, fmt.Errorf("human policy %s needs a GitHub client", opts.Humans)
		}
	default:
		return nil, fmt.Errorf("unknown human policy %q (want allow, deny or github-verified)", opts.Humans)
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
	sha, authorEmail, committerEmail, committerName, subject string
	trailers                                                 []string
}

func listCommits(ctx context.Context, opts Options) ([]commitInfo, error) {
	// %x1f separates fields and %x1e separates trailer values; neither can
	// appear in a subject or a trailer. The subject goes last.
	format := "--format=%H%x1f%ae%x1f%ce%x1f%cn%x1f%(trailers:key=" + TrailerKey + ",valueonly,unfold,separator=%x1e)%x1f%s"
	out, err := git(ctx, opts, "log", "--no-show-signature", format, opts.Range, "--")
	if err != nil {
		return nil, err
	}
	var commits []commitInfo
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\x1f", 6)
		if len(f) != 6 {
			return nil, fmt.Errorf("unexpected git log line %q", line)
		}
		c := commitInfo{sha: f[0], authorEmail: f[1], committerEmail: f[2], committerName: f[3], subject: f[5]}
		for _, t := range strings.Split(f[4], "\x1e") {
			if t = strings.TrimSpace(t); t != "" {
				c.trailers = append(c.trailers, t)
			}
		}
		commits = append(commits, c)
	}
	return commits, nil
}

// parseIdent splits "Name <email>".
func parseIdent(s string) (name, email string, ok bool) {
	lt, gt := strings.IndexByte(s, '<'), strings.LastIndexByte(s, '>')
	if lt < 0 || gt < lt || strings.TrimSpace(s[gt+1:]) != "" {
		return "", "", false
	}
	return strings.TrimSpace(s[:lt]), strings.TrimSpace(s[lt+1 : gt]), true
}

var goodSig = regexp.MustCompile(`Good "git" signature for (\S+) with (\S+) key (SHA256:\S+)`)

func checkCommit(ctx context.Context, opts Options, signersPath string, sf *signers.File, agents map[string]bool, c commitInfo) Result {
	res := Result{Commit: c.sha, Subject: c.subject, Committer: c.committerEmail, CommitterName: c.committerName,
		Author: c.authorEmail, AgentTrailers: c.trailers}
	committer := strings.ToLower(c.committerEmail)

	if !agents[committer] {
		if d := domainOf(committer); d != "" && containsFold(opts.AgentDomains, d) {
			return fail(res, fmt.Sprintf("committer %s is on agent domain %s but is not a registered agent", c.committerEmail, d))
		}
		if len(c.trailers) > 0 {
			// An Agent: line is a claim of agent authorship; only a
			// signature from that agent can back it.
			return fail(res, fmt.Sprintf("%s: %s trailer on a commit not signed by a registered agent", TrailerKey, c.trailers[0]))
		}
		switch opts.Humans {
		case HumansDeny:
			return fail(res, "committer is not a registered agent and human commits are denied")
		case HumansGitHubVerified:
			ok, detail, err := opts.HumanCheck(ctx, c.sha)
			if err != nil {
				// Fail closed: an unreachable API must not let commits through.
				return fail(res, "could not check GitHub verification: "+err.Error())
			}
			if !ok {
				return fail(res, "human commit is not Verified on GitHub ("+detail+")")
			}
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
	want := Trailer(c.committerName, res.Agent)
	for _, t := range c.trailers {
		name, email, ok := parseIdent(t)
		if !ok {
			return fail(res, fmt.Sprintf("malformed %s trailer %q (want %q)", TrailerKey, t, want))
		}
		if !strings.EqualFold(email, res.Agent) || name != c.committerName {
			return fail(res, fmt.Sprintf("%s trailer says %q but the commit is signed by %s <%s>", TrailerKey, t, c.committerName, res.Agent))
		}
	}
	if opts.RequireTrailer && len(c.trailers) == 0 {
		return fail(res, fmt.Sprintf("missing %s trailer (want %q)", TrailerKey, want))
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

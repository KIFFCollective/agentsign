package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/KIFFCollective/agentsign/internal/github"
	"github.com/KIFFCollective/agentsign/internal/verify"
)

func (a *app) cmdVerify(args []string) error {
	fs := a.newFlags("verify", "verify [flags] <revision range, e.g. origin/main..HEAD>")
	signersFile := fs.String("signers", ".github/allowed_signers", "allowed_signers file")
	repo := fs.String("repo", "", "repository folder (default: current)")
	humans := fs.String("humans", "allow", "policy for non-agent committers: allow, deny or github-verified (needs $GITHUB_TOKEN)")
	ghRepo := fs.String("github-repo", "", "owner/name for github-verified lookups (default $GITHUB_REPOSITORY)")
	authorMatch := fs.Bool("require-author-match", false, "agent commits must have author == committer")
	format := fs.String("format", "text", "output: text, markdown or json")
	summary := fs.String("summary", "", "also append a markdown report to FILE (e.g. $GITHUB_STEP_SUMMARY)")
	var domains multiFlag
	fs.Var(&domains, "agent-domain", "email domain reserved for agents; unknown committers there fail (repeatable)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fs.Usage()
		return exitError(2)
	}
	c, err := a.config()
	if err != nil {
		return err
	}
	opts := verify.Options{
		RepoDir:            *repo,
		Range:              pos[0],
		SignersFile:        *signersFile,
		Humans:             verify.HumanPolicy(*humans),
		AgentDomains:       domains,
		RequireAuthorMatch: *authorMatch,
		SSHKeygen:          c.SSHKeygen,
	}
	if opts.Humans == verify.HumansGitHubVerified {
		check, err := a.githubHumanCheck(*ghRepo)
		if err != nil {
			return err
		}
		opts.HumanCheck = check
		if len(domains) == 0 {
			a.warnf("with --humans github-verified, set --agent-domain: GitHub marks ANY email on the machine account " +
				"as Verified, so an agent email missing from allowed_signers would pass as a human")
		}
	}
	rep, err := verify.Run(context.Background(), opts)
	if err != nil {
		return err
	}
	switch *format {
	case "text":
		err = rep.WriteText(a.stdout)
	case "markdown":
		err = rep.WriteMarkdown(a.stdout)
	case "json":
		err = rep.WriteJSON(a.stdout)
	default:
		return fmt.Errorf("unknown format %q", *format)
	}
	if err != nil {
		return err
	}
	if *summary != "" {
		f, err := os.OpenFile(*summary, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		werr := rep.WriteMarkdown(f)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
	}
	if !rep.OK() {
		return exitError(1)
	}
	return nil
}

// githubHumanCheck asks the GitHub API whether a commit is Verified.
func (a *app) githubHumanCheck(repo string) (verify.HumanCheck, error) {
	if repo == "" {
		repo = a.getenv("GITHUB_REPOSITORY")
	}
	if repo == "" {
		return nil, errors.New("--humans github-verified needs --github-repo owner/name (or $GITHUB_REPOSITORY)")
	}
	client := github.NewFromEnv(a.getenv)
	if client.Token == "" {
		a.warnf("no GITHUB_TOKEN or GH_TOKEN set; unauthenticated API calls are heavily rate limited and can't see private repos")
	}
	return func(ctx context.Context, sha string) (bool, string, error) {
		v, err := client.CommitVerification(ctx, repo, sha)
		if err != nil {
			return false, "", err
		}
		return v.Verified, v.Reason, nil
	}, nil
}

func (a *app) cmdGitHub(args []string) error {
	if len(args) > 0 && args[0] == "sync" {
		return fmt.Errorf("github sync is planned for v0.2; for now add keys and emails by hand (see 'agentsign agent show <id>')")
	}
	return fmt.Errorf("usage: agentsign github sync")
}

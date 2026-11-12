package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/KIFFCollective/agentsign/internal/credential"
	"github.com/KIFFCollective/agentsign/internal/resolve"
	"github.com/KIFFCollective/agentsign/internal/sign"
	"github.com/KIFFCollective/agentsign/internal/store"
	"github.com/KIFFCollective/agentsign/internal/verify"
)

// cmdCredential is the git credential helper. It must never break other
// helpers: anything it doesn't handle produces no output and exit 0.
func (a *app) cmdCredential(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: agentsign credential get|store|erase")
	}
	switch args[0] {
	case "store", "erase":
		_, _ = io.Copy(io.Discard, a.stdin)
		return nil
	case "get":
	default:
		// Future operations: ignore, per the helper protocol.
		_, _ = io.Copy(io.Discard, a.stdin)
		return nil
	}

	req, err := credential.Parse(a.stdin)
	if err != nil {
		a.warnf("credential: %v", err)
		return nil
	}
	if req.Protocol != "https" {
		a.debugf("credential: ignoring protocol %q", req.Protocol)
		return nil
	}
	r, err := a.resolveAgent()
	if errors.Is(err, resolve.ErrNoAgent) {
		a.debugf("credential: no agent identified")
		return nil
	}
	if err != nil {
		a.warnf("credential: %v", err)
		return nil
	}
	_, ag, err := a.loadAgent(r.ID)
	if err != nil {
		a.warnf("credential: %v", err)
		return nil
	}
	if ag.Revoked {
		a.warnf("credential: agent %s is revoked", ag.ID)
		return nil
	}
	if !ag.AllowsHost(req.Hostname()) {
		a.debugf("credential: agent %s not configured for host %s", ag.ID, req.Hostname())
		return nil
	}
	if len(ag.Repos) > 0 {
		repo := req.Repo()
		if repo == "" {
			a.warnf("credential: agent %s is limited to certain repos but git sent no path; set credential.useHttpPath=true (agentsign init does this)", ag.ID)
			return nil
		}
		if !ag.AllowsRepo(repo) {
			a.warnf("credential: agent %s is not allowed to access %s", ag.ID, repo)
			return nil
		}
	}
	if ag.TokenFile == "" {
		if err := store.CheckPerm(fileIn(ag.Dir, store.TokenFile)); err != nil {
			a.warnf("credential: %v", err)
			return nil
		}
	}
	tok, err := ag.Token()
	if err != nil {
		a.warnf("credential: agent %s: %v", ag.ID, err)
		return nil
	}
	user := ag.Username
	if user == "" {
		user = "agentsign"
	}
	a.debugf("credential: supplying token for agent %s (%s via %s)", ag.ID, r.Source, r.Detail)
	return (&credential.Response{Username: user, Password: tok, Expiry: ag.TokenExpiry}).Write(a.stdout)
}

// cmdSign stands in for ssh-keygen as git's gpg.ssh.program.
func (a *app) cmdSign(args []string) error {
	c, err := a.config()
	if err != nil {
		return err
	}
	op := sign.Classify(args)
	if op == sign.OpPassthrough {
		return a.runSSHKeygen(c.SSHKeygen, args)
	}

	_, ag, err := a.currentAgent()
	if errors.Is(err, resolve.ErrNoAgent) {
		return errors.New("refusing to sign: no agent identified (set AGENTSIGN_AGENT or pass --agent)")
	}
	if err != nil {
		return fmt.Errorf("refusing to sign: %w", err)
	}
	if err := store.CheckPerm(ag.KeyPath()); err != nil {
		return fmt.Errorf("refusing to sign: %w", err)
	}
	if op == sign.OpSign {
		if err := sign.CheckBufferIdentity(args, ag.Email); err != nil {
			return fmt.Errorf("refusing to sign as agent %s: %w", ag.ID, err)
		}
	}
	a.debugf("sign: %s with key of agent %s", op, ag.ID)
	return a.runSSHKeygen(c.SSHKeygen, sign.Rewrite(args, ag.KeyPath()))
}

func (a *app) runSSHKeygen(bin string, args []string) error {
	code, err := sign.Run(bin, args, a.stdin, a.stdout, a.stderr)
	if err != nil {
		return fmt.Errorf("running %s: %w", bin, err)
	}
	if code != 0 {
		return exitError(code)
	}
	return nil
}

// identityEnv returns GIT_AUTHOR_* and GIT_COMMITTER_* for an agent.
func identityEnv(ag *store.Agent) [][2]string {
	name := ag.Name
	if name == "" {
		name = ag.ID
	}
	return [][2]string{
		{"GIT_AUTHOR_NAME", name},
		{"GIT_AUTHOR_EMAIL", ag.Email},
		{"GIT_COMMITTER_NAME", name},
		{"GIT_COMMITTER_EMAIL", ag.Email},
	}
}

func (a *app) cmdEnv(args []string) error {
	fs := a.newFlags("env", "env [--format sh|pwsh|plain]")
	format := fs.String("format", "sh", "output format: sh, pwsh or plain (KEY=VALUE)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, ag, err := a.currentAgent()
	if err != nil {
		return err
	}
	for _, kv := range identityEnv(ag) {
		switch *format {
		case "sh":
			fmt.Fprintf(a.stdout, "export %s=%s\n", kv[0], shQuote(kv[1]))
		case "pwsh":
			fmt.Fprintf(a.stdout, "$env:%s = '%s'\n", kv[0], strings.ReplaceAll(kv[1], "'", "''"))
		case "plain":
			fmt.Fprintf(a.stdout, "%s=%s\n", kv[0], kv[1])
		default:
			return fmt.Errorf("unknown format %q", *format)
		}
	}
	return nil
}

// cmdGit runs the real git with the agent's identity in the environment.
func (a *app) cmdGit(args []string) error {
	_, ag, err := a.currentAgent()
	if err != nil {
		return err
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	if self, err := os.Executable(); err == nil && sameFile(self, gitBin) {
		return errors.New("the git on PATH is agentsign itself; refusing to recurse")
	}
	ident := identityEnv(ag)
	args = injectTrailer(args, verify.Trailer(ident[0][1], ag.Email))
	cmd := exec.Command(gitBin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.stdin, a.stdout, a.stderr
	cmd.Env = os.Environ()
	for _, kv := range identityEnv(ag) {
		cmd.Env = append(cmd.Env, kv[0]+"="+kv[1])
	}
	// Pin the agent so the helper and signer agree with this identity even
	// if detection would otherwise differ (e.g. git -C elsewhere).
	cmd.Env = append(cmd.Env, "AGENTSIGN_AGENT="+ag.ID)
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return exitError(ee.ExitCode())
	}
	return err
}

// gitSubcommand returns the index of git's subcommand in args (skipping
// global options such as -C dir and -c key=value), or -1.
func gitSubcommand(args []string) int {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-C" || a == "-c" || a == "--git-dir" || a == "--work-tree" || a == "--namespace" || a == "--config-env":
			i++ // option takes the next argument
		case strings.HasPrefix(a, "-"):
		default:
			return i
		}
	}
	return -1
}

// injectTrailer adds --trailer to a git commit invocation. git's default
// trailer.ifExists (addIfDifferentNeighbor) keeps it from being duplicated
// on --amend.
func injectTrailer(args []string, trailer string) []string {
	i := gitSubcommand(args)
	if i < 0 || args[i] != "commit" {
		return args
	}
	out := append([]string{}, args[:i+1]...)
	out = append(out, "--trailer", trailer)
	return append(out, args[i+1:]...)
}

// cmdTrailer prints the agent's commit trailer, for agents that write their
// own commit messages.
func (a *app) cmdTrailer(args []string) error {
	fs := a.newFlags("trailer", "trailer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, ag, err := a.currentAgent()
	if err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, verify.Trailer(identityEnv(ag)[0][1], ag.Email))
	return nil
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func sameFile(a, b string) bool {
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}

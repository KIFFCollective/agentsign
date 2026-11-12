// Command agentsign gives each coding agent its own git identity, signing key
// and token on a single GitHub machine account.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/KIFFCollective/agentsign/internal/config"
	"github.com/KIFFCollective/agentsign/internal/resolve"
	"github.com/KIFFCollective/agentsign/internal/store"
)

// Set by the release build.
var version = "dev"

const usage = `agentsign: per-agent git identities on one GitHub account

Usage:
  agentsign [--config FILE] [--agent ID] <command> [args]

Git integration (normally run by git, not by hand):
  credential get|store|erase   git credential helper
  sign <ssh-keygen args>       gpg.ssh.program wrapper (also: agentsign -Y sign ...)
  env [--format sh|pwsh|plain] print GIT_AUTHOR_* / GIT_COMMITTER_* for the agent
  git <args>                   run git as the agent (commit gets an Agent: trailer);
                               also runs this way when invoked as "git" (symlink shim)
  trailer                      print the agent's commit trailer: Agent: Name <email>

Setup and management:
  init                         create the config, agent store and shared git config
  agent add <id> --name N --email E [--repo owner/name|owner/*]...
  agent token <id> [--expires YYYY-MM-DD]   (token read from stdin)
  agent list | show <id> | rotate <id> | revoke <id>
  agent repo list|add|remove|restrict|allow-all <id> [repo...]
  signers [--out FILE]         generate allowed_signers for all agents
  verify <range>               check agent commits against allowed_signers
  doctor                       check this host's setup
  github sync                  upload keys and emails (not yet implemented)
  version

The agent is found from --agent, then $AGENTSIGN_AGENT and any env vars named
in the config, then the config's path rules. Run 'agentsign <command> -h' for
command flags.
`

// exitError carries an exit code without printing anything more.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit %d", int(e)) }

type app struct {
	configPath string
	agentFlag  string

	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string
	getwd  func() (string, error)

	cfg *config.Config
}

func main() {
	a := &app{
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr,
		getenv: os.Getenv, getwd: os.Getwd,
	}
	if isGitShim(os.Args[0]) {
		os.Exit(a.finish(a.cmdGitShim(os.Args[1:])))
	}
	os.Exit(a.run(os.Args[1:]))
}

func (a *app) run(args []string) int {
	return a.finish(a.dispatch(args))
}

// finish turns a command's error into an exit code.
func (a *app) finish(err error) int {
	var code exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &code):
		return int(code)
	case errors.Is(err, flag.ErrHelp):
		return 0
	default:
		fmt.Fprintf(a.stderr, "agentsign: %v\n", err)
		return 1
	}
}

// isSSHKeygenCall reports whether git invoked us as gpg.ssh.program. git runs
// that program directly (no shell), so it can't carry a subcommand; ssh-keygen
// arguments always start with a single dash.
func isSSHKeygenCall(args []string) bool {
	if len(args) == 0 {
		return false
	}
	a := args[0]
	return strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && a != "-h"
}

func (a *app) dispatch(args []string) error {
	if isSSHKeygenCall(args) {
		return a.cmdSign(args)
	}
	fs := flag.NewFlagSet("agentsign", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Usage = func() { fmt.Fprint(a.stderr, usage) }
	fs.StringVar(&a.configPath, "config", "", "config file")
	fs.StringVar(&a.agentFlag, "agent", "", "agent id (overrides detection)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return exitError(2)
	}
	cmd, rest := rest[0], rest[1:]
	switch cmd {
	case "credential":
		return a.cmdCredential(rest)
	case "sign":
		return a.cmdSign(rest)
	case "env":
		return a.cmdEnv(rest)
	case "git":
		return a.cmdGit(rest)
	case "trailer":
		return a.cmdTrailer(rest)
	case "init":
		return a.cmdInit(rest)
	case "agent":
		return a.cmdAgent(rest)
	case "signers":
		return a.cmdSigners(rest)
	case "verify":
		return a.cmdVerify(rest)
	case "doctor":
		return a.cmdDoctor(rest)
	case "github":
		return a.cmdGitHub(rest)
	case "version":
		fmt.Fprintln(a.stdout, "agentsign", version)
		return nil
	case "help":
		fmt.Fprint(a.stdout, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q (see 'agentsign help')", cmd)
	}
}

func (a *app) config() (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	c, err := config.Load(a.configPath)
	if err != nil {
		return nil, err
	}
	a.cfg = c
	return c, nil
}

// resolveAgent identifies the calling agent.
func (a *app) resolveAgent() (*resolve.Result, error) {
	c, err := a.config()
	if err != nil {
		return nil, err
	}
	cwd, _ := a.getwd()
	return resolve.Resolve(c, resolve.Input{Flag: a.agentFlag, Getenv: a.getenv, Cwd: cwd})
}

// storeFor returns the store holding agent id.
func (a *app) storeFor(id string) (*store.Store, error) {
	c, err := a.config()
	if err != nil {
		return nil, err
	}
	return store.New(c.StoreFor(id)), nil
}

// loadAgent loads an agent by id.
func (a *app) loadAgent(id string) (*store.Store, *store.Agent, error) {
	s, err := a.storeFor(id)
	if err != nil {
		return nil, nil, err
	}
	ag, err := s.Load(id)
	if err != nil {
		return nil, nil, err
	}
	return s, ag, nil
}

// currentAgent resolves and loads the calling agent, refusing revoked ones.
func (a *app) currentAgent() (*store.Store, *store.Agent, error) {
	r, err := a.resolveAgent()
	if err != nil {
		return nil, nil, err
	}
	s, ag, err := a.loadAgent(r.ID)
	if err != nil {
		return nil, nil, err
	}
	if ag.Revoked {
		return nil, nil, fmt.Errorf("agent %s is revoked", ag.ID)
	}
	return s, ag, nil
}

func (a *app) debugf(format string, args ...any) {
	if a.getenv("AGENTSIGN_DEBUG") != "" {
		fmt.Fprintf(a.stderr, "agentsign: "+format+"\n", args...)
	}
}

func (a *app) warnf(format string, args ...any) {
	fmt.Fprintf(a.stderr, "agentsign: "+format+"\n", args...)
}

// newFlags builds a subcommand flag set that prints errors to stderr.
func (a *app) newFlags(name, synopsis string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Usage = func() {
		fmt.Fprintf(a.stderr, "Usage: agentsign %s\n", synopsis)
		fs.PrintDefaults()
	}
	return fs
}

// parseInterleaved parses flags that may appear before or after positional
// arguments ("agent add dev --email x" and "agent add --email x dev").
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

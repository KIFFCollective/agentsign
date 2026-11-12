package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/KIFFCollective/agentsign/internal/config"
	"github.com/KIFFCollective/agentsign/internal/signers"
	"github.com/KIFFCollective/agentsign/internal/store"
)

func fileIn(dir, name string) string { return filepath.Join(dir, name) }

func (a *app) cmdAgent(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: agentsign agent add|token|list|show|rotate|revoke|repo")
	}
	switch args[0] {
	case "add":
		return a.agentAdd(args[1:])
	case "token":
		return a.agentToken(args[1:])
	case "list", "ls":
		return a.agentList(args[1:])
	case "show":
		return a.agentShow(args[1:])
	case "rotate":
		return a.agentRotate(args[1:])
	case "revoke":
		return a.agentRevoke(args[1:])
	case "repo":
		return a.agentRepo(args[1:])
	default:
		return fmt.Errorf("unknown agent command %q", args[0])
	}
}

func oneID(fs *flag.FlagSet, pos []string) (string, error) {
	if len(pos) != 1 {
		fs.Usage()
		return "", exitError(2)
	}
	return pos[0], nil
}

func (a *app) agentAdd(args []string) error {
	fs := a.newFlags("agent add", "agent add <id> --name NAME --email EMAIL [--repo owner/name]... [--host HOST]... [--username LOGIN]")
	name := fs.String("name", "", "display name for commits (default: the id)")
	email := fs.String("email", "", "commit email; must be a verified email on the machine account")
	username := fs.String("username", "", "username sent with the token (e.g. the machine account login)")
	var repos, hosts multiFlag
	fs.Var(&repos, "repo", "repo the token may be used for, owner/name or owner/* (repeatable; omit for all repos)")
	fs.Var(&hosts, "host", "host the token may be sent to (repeatable; default github.com)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	id, err := oneID(fs, pos)
	if err != nil {
		return err
	}
	if *email == "" {
		return errors.New("--email is required")
	}
	if addr, err := mail.ParseAddress(*email); err != nil || addr.Address != *email {
		return fmt.Errorf("invalid email %q", *email)
	}
	ag := &store.Agent{ID: id, Name: *name, Email: *email, Username: *username, Hosts: hosts}
	if _, err := ag.AddRepos(repos...); err != nil {
		return err
	}
	if err := a.checkEmailUnused(*email); err != nil {
		return err
	}
	s, err := a.storeFor(id)
	if err != nil {
		return err
	}
	if err := s.Create(ag); err != nil {
		return err
	}
	pub, err := ag.PublicKey()
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "Created agent %s in %s\n\n", id, ag.Dir)
	a.printGitHubSteps(ag, pub)
	return nil
}

func (a *app) printGitHubSteps(ag *store.Agent, pub string) {
	fmt.Fprintf(a.stdout, `Next, signed in to GitHub as the machine account:

  1. Add and verify the email (Settings > Emails):
       %s
  2. Add the signing key (Settings > SSH and GPG keys > New SSH key, type "Signing key"):
       %s
  3. Create a fine-grained token limited to this agent's repos, then store it:
       agentsign agent token %s < token.txt
  4. Regenerate allowed_signers in each repo:
       agentsign signers --out .github/allowed_signers
`, ag.Email, pub, ag.ID)
}

// checkEmailUnused refuses an email already used by another agent: two
// agents sharing an email would defeat per-agent verification.
func (a *app) checkEmailUnused(email string) error {
	agents, err := a.allAgents()
	if err != nil {
		return err
	}
	for _, other := range agents {
		if strings.EqualFold(other.Email, email) {
			return fmt.Errorf("email %s is already used by agent %s", email, other.ID)
		}
	}
	return nil
}

// allAgents lists agents across the store. If the store path contains
// {agent}, every matching folder is searched.
func (a *app) allAgents() ([]*store.Agent, error) {
	c, err := a.config()
	if err != nil {
		return nil, err
	}
	if !strings.Contains(c.Store, config.AgentPlaceholder) {
		return store.New(c.Store).List()
	}
	roots, err := filepath.Glob(strings.ReplaceAll(c.Store, config.AgentPlaceholder, "*"))
	if err != nil {
		return nil, err
	}
	var out []*store.Agent
	for _, root := range roots {
		list, err := store.New(root).List()
		if err != nil {
			return nil, err
		}
		for _, ag := range list {
			// Only the agent whose own expanded root this is.
			if filepath.Clean(c.StoreFor(ag.ID)) == filepath.Clean(root) {
				out = append(out, ag)
			}
		}
	}
	return out, nil
}

func (a *app) agentToken(args []string) error {
	fs := a.newFlags("agent token", "agent token <id> [--expires YYYY-MM-DD] < token.txt")
	expires := fs.String("expires", "", "token expiry date (YYYY-MM-DD or RFC 3339), used for warnings and password_expiry_utc")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	id, err := oneID(fs, pos)
	if err != nil {
		return err
	}
	var exp *time.Time
	if *expires != "" {
		t, err := parseDate(*expires)
		if err != nil {
			return err
		}
		exp = &t
	}
	s, ag, err := a.loadAgent(id)
	if err != nil {
		return err
	}
	if ag.Revoked {
		return fmt.Errorf("agent %s is revoked", id)
	}
	if f, ok := a.stdin.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprintf(a.stderr, "Paste the token for %s, then press Enter:\n", id)
		}
	}
	line, err := bufio.NewReader(io.LimitReader(a.stdin, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if err := s.SetToken(ag, line, exp); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "Stored token for agent %s\n", id)
	return nil
}

func parseDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date %q (want YYYY-MM-DD)", s)
	}
	return t.UTC(), nil
}

func (a *app) agentList(args []string) error {
	fs := a.newFlags("agent list", "agent list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	agents, err := a.allAgents()
	if err != nil {
		return err
	}
	if len(agents) == 0 {
		c, _ := a.config()
		fmt.Fprintf(a.stderr, "No agents in %s\n", c.Store)
		return nil
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tEMAIL\tTOKEN\tSTATUS")
	for _, ag := range agents {
		status := "active"
		if ag.Revoked {
			status = "revoked"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", ag.ID, ag.Name, ag.Email, tokenState(ag), status)
	}
	return tw.Flush()
}

func tokenState(ag *store.Agent) string {
	if _, err := ag.Token(); err != nil {
		return "missing"
	}
	if ag.TokenExpiry == nil {
		return "set"
	}
	left := time.Until(*ag.TokenExpiry)
	switch {
	case left <= 0:
		return "expired"
	case left < 7*24*time.Hour:
		return fmt.Sprintf("expires in %dd", int(left.Hours()/24))
	default:
		return "until " + ag.TokenExpiry.Format("2006-01-02")
	}
}

func (a *app) agentShow(args []string) error {
	fs := a.newFlags("agent show", "agent show <id>")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	id, err := oneID(fs, pos)
	if err != nil {
		return err
	}
	_, ag, err := a.loadAgent(id)
	if err != nil {
		return err
	}
	w := a.stdout
	fmt.Fprintf(w, "id:        %s\n", ag.ID)
	fmt.Fprintf(w, "name:      %s\n", ag.Name)
	fmt.Fprintf(w, "email:     %s\n", ag.Email)
	fmt.Fprintf(w, "folder:    %s\n", ag.Dir)
	fmt.Fprintf(w, "hosts:     %s\n", strings.Join(ag.Hosts, ", "))
	repos := "any (limited by the token's own scope)"
	if len(ag.Repos) > 0 {
		repos = strings.Join(ag.Repos, ", ")
	}
	fmt.Fprintf(w, "repos:     %s\n", repos)
	fmt.Fprintf(w, "token:     %s\n", tokenState(ag))
	if ag.Revoked {
		fmt.Fprintf(w, "status:    revoked %s\n", fmtTime(ag.RevokedAt))
		return nil
	}
	pub, err := ag.PublicKey()
	if err != nil {
		fmt.Fprintf(w, "key:       missing (%v)\n", err)
		return nil
	}
	fp, _ := ag.Fingerprint()
	fmt.Fprintf(w, "key:       %s\n", fp)
	fmt.Fprintf(w, "public:    %s\n", pub)
	return nil
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

func (a *app) agentRotate(args []string) error {
	fs := a.newFlags("agent rotate", "agent rotate <id>")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	id, err := oneID(fs, pos)
	if err != nil {
		return err
	}
	s, ag, err := a.loadAgent(id)
	if err != nil {
		return err
	}
	if ag.Revoked {
		return fmt.Errorf("agent %s is revoked; add a new agent instead", id)
	}
	oldFP, _ := ag.Fingerprint()
	if err := s.GenerateKey(ag); err != nil {
		return err
	}
	pub, err := ag.PublicKey()
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, `Rotated signing key for agent %s.

  1. Add the new signing key to the machine account on GitHub:
       %s
  2. Regenerate allowed_signers in each repo (agentsign signers --out ...).
  3. Once merged, delete the old key from GitHub: %s
     Commits signed with the old key will then show as Unverified on GitHub
     and fail agentsign verify.

The old private key is kept at %s.old until the next rotate.
`, id, pub, oldFP, ag.KeyPath())
	return nil
}

func (a *app) agentRevoke(args []string) error {
	fs := a.newFlags("agent revoke", "agent revoke <id> --yes")
	yes := fs.Bool("yes", false, "confirm: delete the agent's key and token")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	id, err := oneID(fs, pos)
	if err != nil {
		return err
	}
	s, ag, err := a.loadAgent(id)
	if err != nil {
		return err
	}
	fp, _ := ag.Fingerprint()
	if !*yes {
		return fmt.Errorf("this deletes the key and token of agent %s; re-run with --yes to confirm", id)
	}
	if err := s.Revoke(ag); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, `Revoked agent %s locally (key and token deleted).

Finish on GitHub, signed in as the machine account:
  1. Settings > Developer settings > Fine-grained tokens: revoke this agent's token.
  2. Settings > SSH and GPG keys: delete signing key %s
  3. Settings > Emails: optionally remove %s
  4. Regenerate allowed_signers in each repo (agentsign signers --out ...);
     the agent stays listed as revoked so its email can't pass as a human.
`, id, orDash(fp), ag.Email)
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (a *app) cmdSigners(args []string) error {
	fs := a.newFlags("signers", "signers [--out FILE]")
	out := fs.String("out", "", "write to FILE instead of stdout (e.g. .github/allowed_signers)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	data, err := a.buildSigners()
	if err != nil {
		return err
	}
	if *out == "" {
		_, err := a.stdout.Write(data)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(a.stderr, "Wrote %s\n", *out)
	return nil
}

func (a *app) buildSigners() ([]byte, error) {
	agents, err := a.allAgents()
	if err != nil {
		return nil, err
	}
	f := &signers.File{}
	for _, ag := range agents {
		if ag.Revoked {
			f.Revoked = append(f.Revoked, ag.Email)
			continue
		}
		pub, err := ag.PublicKey()
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", ag.ID, err)
		}
		comment := ag.ID
		if ag.Name != "" && ag.Name != ag.ID {
			comment += " (" + ag.Name + ")"
		}
		e, err := signers.EntryFromPublicKey(ag.Email, pub, comment)
		if err != nil {
			return nil, err
		}
		f.Entries = append(f.Entries, e)
	}
	return signers.Format(f), nil
}

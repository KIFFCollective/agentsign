package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/KIFFCollective/agentsign/internal/resolve"
	"github.com/KIFFCollective/agentsign/internal/store"
)

type checkLevel int

const (
	lvlOK checkLevel = iota
	lvlWarn
	lvlFail
)

type doctor struct {
	a     *app
	fails int
	warns int
}

func (d *doctor) report(l checkLevel, name, format string, args ...any) {
	tag := map[checkLevel]string{lvlOK: "ok  ", lvlWarn: "warn", lvlFail: "FAIL"}[l]
	fmt.Fprintf(d.a.stdout, "[%s] %-14s %s\n", tag, name, fmt.Sprintf(format, args...))
	switch l {
	case lvlFail:
		d.fails++
	case lvlWarn:
		d.warns++
	}
}

func (a *app) cmdDoctor(args []string) error {
	fs := a.newFlags("doctor", "doctor [--remote URL] [--signers FILE]")
	remote := fs.String("remote", "", "HTTPS repo URL to test the agent's token against (git ls-remote)")
	signersFile := fs.String("signers", "", "allowed_signers file to check is up to date")
	if err := fs.Parse(args); err != nil {
		return err
	}
	d := &doctor{a: a}
	d.checkGit()

	c, err := a.config()
	if err != nil {
		d.report(lvlFail, "config", "%v", err)
		return exitError(1)
	}
	if c.Path == "" {
		d.report(lvlWarn, "config", "no config file found; using defaults (store %s)", c.Store)
	} else {
		d.report(lvlOK, "config", "%s", c.Path)
	}
	if p, err := exec.LookPath(c.SSHKeygen); err != nil {
		d.report(lvlFail, "ssh-keygen", "%s not found: install openssh-client", c.SSHKeygen)
	} else {
		d.report(lvlOK, "ssh-keygen", "%s", p)
	}
	d.checkGitConfig()

	r, err := a.resolveAgent()
	switch {
	case errors.Is(err, resolve.ErrNoAgent):
		d.report(lvlWarn, "agent", "no agent identified (checked --agent, %s, path rules); per-agent checks skipped", strings.Join(c.EnvVars(), ", "))
	case err != nil:
		d.report(lvlFail, "agent", "%v", err)
	default:
		d.checkAgent(r, *remote)
	}

	if *signersFile != "" {
		d.checkSigners(*signersFile)
	}

	fmt.Fprintf(a.stdout, "\n%d failed, %d warnings\n", d.fails, d.warns)
	if d.fails > 0 {
		return exitError(1)
	}
	return nil
}

var gitVersionRE = regexp.MustCompile(`git version (\d+)\.(\d+)`)

func (d *doctor) checkGit() {
	out, err := exec.Command("git", "version").Output()
	if err != nil {
		d.report(lvlFail, "git", "git not found: %v", err)
		return
	}
	m := gitVersionRE.FindStringSubmatch(string(out))
	if m == nil {
		d.report(lvlWarn, "git", "unrecognised version %q", strings.TrimSpace(string(out)))
		return
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 2 || (major == 2 && minor < 34) {
		d.report(lvlFail, "git", "%s.%s is too old; SSH signing needs 2.34+", m[1], m[2])
		return
	}
	d.report(lvlOK, "git", "%s.%s", m[1], m[2])
}

func gitConfigGet(args ...string) []string {
	out, err := exec.Command("git", append([]string{"config"}, args...)...).Output()
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
}

func (d *doctor) checkGitConfig() {
	if v := gitConfigGet("--get", "gpg.format"); len(v) == 0 || v[0] != "ssh" {
		d.report(lvlFail, "gpg.format", "want ssh (run agentsign init)")
	} else {
		d.report(lvlOK, "gpg.format", "ssh")
	}
	if v := gitConfigGet("--get", "gpg.ssh.program"); len(v) == 0 || !strings.Contains(strings.ToLower(filepath.Base(v[0])), "agentsign") {
		d.report(lvlFail, "gpg.ssh.prog", "not set to agentsign (run agentsign init)")
	} else {
		d.report(lvlOK, "gpg.ssh.prog", "%s", v[0])
	}
	if v := gitConfigGet("--get", "commit.gpgSign"); len(v) == 0 || v[0] != "true" {
		d.report(lvlWarn, "commit.gpgSign", "commits won't be signed by default")
	}
	helpers := gitConfigGet("--get-all", "credential.https://github.com.helper")
	switch {
	case len(helpers) == 0 || !strings.Contains(helpers[len(helpers)-1], "agentsign"):
		d.report(lvlFail, "credential", "agentsign is not the github.com credential helper (run agentsign init)")
	case len(helpers) < 2 || helpers[len(helpers)-2] != "":
		d.report(lvlWarn, "credential", "no empty helper before agentsign; inherited helpers may supply human credentials")
	default:
		d.report(lvlOK, "credential", "%s", helpers[len(helpers)-1])
	}
	if v := gitConfigGet("--get", "credential.https://github.com.useHttpPath"); len(v) == 0 || v[0] != "true" {
		d.report(lvlWarn, "useHttpPath", "not set; per-repo token limits can't be enforced")
	}
	if v := gitConfigGet("--get", "user.email"); len(v) > 0 && v[0] != "" {
		d.report(lvlWarn, "user.email", "set to %s in git config; agents should get identity from the environment", v[0])
	}
}

func (d *doctor) checkAgent(r *resolve.Result, remote string) {
	a := d.a
	d.report(lvlOK, "agent", "%s (from %s %s)", r.ID, r.Source, r.Detail)
	_, ag, err := a.loadAgent(r.ID)
	if err != nil {
		d.report(lvlFail, "agent store", "%v", err)
		return
	}
	if ag.Revoked {
		d.report(lvlFail, "agent store", "agent %s is revoked", ag.ID)
		return
	}
	d.report(lvlOK, "agent store", "%s", ag.Dir)

	keyOK := true
	if err := store.CheckPerm(ag.KeyPath()); err != nil {
		d.report(lvlFail, "signing key", "%v", err)
		keyOK = false
	} else if fp, err := ag.Fingerprint(); err != nil {
		d.report(lvlFail, "signing key", "%v", err)
		keyOK = false
	} else {
		d.report(lvlOK, "signing key", "%s", fp)
	}
	if keyOK {
		d.checkTestSignature(ag)
	}

	if ag.TokenFile == "" {
		if err := store.CheckPerm(fileIn(ag.Dir, store.TokenFile)); err != nil {
			d.report(lvlFail, "token", "%v", err)
			return
		}
	}
	if _, err := ag.Token(); err != nil {
		d.report(lvlFail, "token", "%v (agentsign agent token %s)", err, ag.ID)
		return
	}
	switch {
	case ag.TokenExpiry == nil:
		d.report(lvlWarn, "token", "present; expiry unknown (store it with --expires)")
	case time.Until(*ag.TokenExpiry) <= 0:
		d.report(lvlFail, "token", "expired %s", ag.TokenExpiry.Format("2006-01-02"))
	case time.Until(*ag.TokenExpiry) < 7*24*time.Hour:
		d.report(lvlWarn, "token", "expires %s", ag.TokenExpiry.Format("2006-01-02"))
	default:
		d.report(lvlOK, "token", "valid until %s", ag.TokenExpiry.Format("2006-01-02"))
	}

	if remote != "" {
		cmd := exec.Command("git", "ls-remote", "--heads", remote)
		cmd.Env = append(os.Environ(), "AGENTSIGN_AGENT="+ag.ID, "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			d.report(lvlFail, "remote", "%s: %s", remote, firstLine(string(out)))
		} else {
			d.report(lvlOK, "remote", "%s reachable with the agent's token", remote)
		}
	}
}

// checkTestSignature signs a scratch buffer with the agent's key and checks
// the signature, using the same ssh-keygen calls git makes.
func (d *doctor) checkTestSignature(ag *store.Agent) {
	c, _ := d.a.config()
	dir, err := os.MkdirTemp("", "agentsign-doctor")
	if err != nil {
		d.report(lvlFail, "test sign", "%v", err)
		return
	}
	defer os.RemoveAll(dir)
	buf := filepath.Join(dir, "payload")
	payload := []byte("agentsign doctor test\n")
	if err := os.WriteFile(buf, payload, 0o600); err != nil {
		d.report(lvlFail, "test sign", "%v", err)
		return
	}
	if out, err := exec.Command(c.SSHKeygen, "-Y", "sign", "-n", "git", "-f", ag.KeyPath(), buf).CombinedOutput(); err != nil {
		d.report(lvlFail, "test sign", "ssh-keygen -Y sign: %s", firstLine(string(out)))
		return
	}
	cmd := exec.Command(c.SSHKeygen, "-Y", "check-novalidate", "-n", "git", "-s", buf+".sig")
	cmd.Stdin = bytes.NewReader(payload)
	if out, err := cmd.CombinedOutput(); err != nil {
		d.report(lvlFail, "test sign", "signature didn't check: %s", firstLine(string(out)))
		return
	}
	d.report(lvlOK, "test sign", "signed and checked a test payload")
}

func (d *doctor) checkSigners(path string) {
	want, err := d.a.buildSigners()
	if err != nil {
		d.report(lvlFail, "signers", "%v", err)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		d.report(lvlFail, "signers", "%v", err)
		return
	}
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
		d.report(lvlFail, "signers", "%s is out of date (agentsign signers --out %s)", path, path)
		return
	}
	d.report(lvlOK, "signers", "%s is up to date", path)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

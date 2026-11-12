package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/KIFFCollective/agentsign/internal/config"
)

// SigningKeyPlaceholder is written to user.signingKey. git passes it to the
// signer as -f, which swaps in the agent's real key.
const SigningKeyPlaceholder = "agentsign"

func (a *app) cmdInit(args []string) error {
	fls := a.newFlags("init", "init [--global|--system|--file FILE] [--store DIR] [--env VAR]... [--path-rule RULE]... [--host HOST]...")
	system := fls.Bool("system", false, "write the shared git config to the system gitconfig")
	global := fls.Bool("global", false, "write the shared git config to the user's ~/.gitconfig (default)")
	file := fls.String("file", "", "write the shared git config to FILE (include it with include.path)")
	storeDir := fls.String("store", "", "agent store folder; may contain {agent}")
	exe := fls.String("exe", "", "path git should use to run agentsign (default: this binary)")
	dryRun := fls.Bool("dry-run", false, "print what would be done without changing anything")
	force := fls.Bool("force", false, "overwrite an existing config file")
	var envs, rules, hosts multiFlag
	fls.Var(&envs, "env", "extra env var naming the agent, e.g. OMB_BOT_ID (repeatable)")
	fls.Var(&rules, "path-rule", "repo location rule, e.g. /workspaces/{agent}/ (repeatable)")
	fls.Var(&hosts, "host", "git host to handle (repeatable; default github.com)")
	if err := fls.Parse(args); err != nil {
		return err
	}
	if fls.NArg() > 0 {
		fls.Usage()
		return exitError(2)
	}

	scope := []string{"--global"}
	switch {
	case btoi(*system)+btoi(*global)+btoi(*file != "") > 1:
		return errors.New("choose one of --system, --global or --file")
	case *system:
		scope = []string{"--system"}
	case *file != "":
		scope = []string{"--file", *file}
	}
	if len(hosts) == 0 {
		hosts = multiFlag{"github.com"}
	}

	// 1. Config file.
	cfgPath := a.configPath
	if cfgPath == "" {
		cfgPath = a.getenv(config.EnvConfig)
	}
	if cfgPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return err
		}
		cfgPath = p
	}
	cfg := &config.Config{Store: *storeDir, Env: envs, PathRules: rules}
	if cfg.Store == "" {
		s, err := config.DefaultStore()
		if err != nil {
			return err
		}
		cfg.Store = s
	}
	for _, r := range rules {
		if strings.Count(r, config.AgentPlaceholder) != 1 {
			return fmt.Errorf("path rule %q must contain %s exactly once", r, config.AgentPlaceholder)
		}
	}
	body, err := cfg.Encode()
	if err != nil {
		return err
	}
	body = "# agentsign config. Shared by every agent on this host.\n" + body

	// 2. Shared git config.
	self := *exe
	if self == "" {
		self, err = os.Executable()
		if err != nil {
			return err
		}
		if strings.Contains(self, "go-build") {
			a.warnf("this looks like a 'go run' binary; pass --exe with the installed path")
		}
	}
	gitPath := filepath.ToSlash(self) // git for Windows runs helpers via sh
	var gitCmds [][]string
	for _, h := range hosts {
		key := "credential.https://" + h
		gitCmds = append(gitCmds,
			[]string{"--unset-all", key + ".helper"},
			// An empty helper first resets helpers inherited from other config
			// files, so the human's credentials are never used as a fallback.
			[]string{"--add", key + ".helper", ""},
			[]string{"--add", key + ".helper", shellWord(gitPath) + " credential"},
			[]string{key + ".useHttpPath", "true"},
		)
	}
	gitCmds = append(gitCmds,
		[]string{"gpg.format", "ssh"},
		[]string{"gpg.ssh.program", gitPath},
		[]string{"user.signingKey", SigningKeyPlaceholder},
		[]string{"commit.gpgSign", "true"},
		[]string{"tag.gpgSign", "true"},
		// Never guess an identity: commits need GIT_AUTHOR_*/GIT_COMMITTER_*.
		[]string{"user.useConfigOnly", "true"},
	)

	if *dryRun {
		fmt.Fprintf(a.stdout, "# would write %s:\n%s\n", cfgPath, body)
		if !strings.Contains(cfg.Store, config.AgentPlaceholder) {
			fmt.Fprintf(a.stdout, "# would create %s\n", cfg.Store)
		}
		fmt.Fprintln(a.stdout, "# would run:")
		for _, c := range gitCmds {
			fmt.Fprintf(a.stdout, "git config %s %s\n", strings.Join(scope, " "), quoteArgs(c))
		}
		return nil
	}

	if err := writeConfig(cfgPath, body, *force); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "Config:  %s\n", cfgPath)
	if !strings.Contains(cfg.Store, config.AgentPlaceholder) {
		if err := os.MkdirAll(cfg.Store, 0o700); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.stdout, "Store:   %s\n", cfg.Store)

	for _, c := range gitCmds {
		full := append([]string{"config"}, scope...)
		full = append(full, c...)
		out, err := exec.Command("git", full...).CombinedOutput()
		if err != nil {
			// --unset-all exits 5 when nothing was set.
			var ee *exec.ExitError
			if c[0] == "--unset-all" && errors.As(err, &ee) && ee.ExitCode() == 5 {
				continue
			}
			return fmt.Errorf("git %s: %w: %s", quoteArgs(full), err, strings.TrimSpace(string(out)))
		}
	}
	fmt.Fprintf(a.stdout, "Git:     shared config written (%s)\n", strings.Join(scope, " "))

	if def, _ := config.DefaultPath(); cfgPath != def {
		fmt.Fprintf(a.stdout, "\nThe config is not in the default location; set %s=%s for every agent.\n", config.EnvConfig, cfgPath)
	}
	fmt.Fprintln(a.stdout, "\nNext: agentsign agent add <id> --name NAME --email EMAIL")
	return nil
}

func writeConfig(path, body string, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", path)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

// shellWord quotes a path for git's shell-run credential helper string.
func shellWord(s string) string {
	if !strings.ContainsAny(s, " \t'\"\\$`!&;|<>()*?[]#~") {
		return s
	}
	return shQuote(s)
}

func quoteArgs(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t'\"") {
			if runtime.GOOS == "windows" {
				out[i] = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
			} else {
				out[i] = shQuote(a)
			}
		} else {
			out[i] = a
		}
	}
	return strings.Join(out, " ")
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

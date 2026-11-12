// Package sign wraps ssh-keygen so it can stand in for git's gpg.ssh.program.
//
// git runs the program as, for example:
//
//	<program> -Y sign -n git -f <user.signingKey> [-U] <buffer-file>
//	<program> -Y verify -n git -f <allowed_signers> -I <principal> -s <sig>
//	<program> -Y find-principals -f <allowed_signers> -s <sig>
//	<program> -Y check-novalidate -n git -s <sig>
//	<program> -l -f <key>                       (key fingerprint)
//
// For sign and fingerprint calls the -f key is swapped for the agent's own
// key and -U (use ssh-agent) is dropped. Everything else passes straight
// through.
package sign

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"os/exec"
	"strings"
)

// Op classifies an ssh-keygen invocation.
type Op string

const (
	OpSign        Op = "sign"
	OpFingerprint Op = "fingerprint"
	OpPassthrough Op = "passthrough"
)

// Classify returns what an ssh-keygen argument list does.
func Classify(args []string) Op {
	for i, a := range args {
		if a == "-Y" && i+1 < len(args) {
			if args[i+1] == "sign" {
				return OpSign
			}
			return OpPassthrough
		}
		if a == "-Ysign" {
			return OpSign
		}
	}
	for _, a := range args {
		if a == "-l" || a == "-lf" {
			return OpFingerprint
		}
	}
	return OpPassthrough
}

// Rewrite replaces the -f key with keyPath and removes -U.
func Rewrite(args []string, keyPath string) []string {
	out := make([]string, 0, len(args)+2)
	sawKey := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-U":
			continue
		case a == "-f" && i+1 < len(args):
			out = append(out, "-f", keyPath)
			i++
			sawKey = true
		case a == "-lf" && i+1 < len(args):
			out = append(out, "-l", "-f", keyPath)
			i++
			sawKey = true
		case strings.HasPrefix(a, "-f") && len(a) > 2:
			out = append(out, "-f", keyPath)
			sawKey = true
		default:
			out = append(out, a)
		}
	}
	if !sawKey {
		// Insert before the trailing file operand.
		if n := len(out); n > 0 && !strings.HasPrefix(out[n-1], "-") {
			out = append(out[:n-1], "-f", keyPath, out[n-1])
		} else {
			out = append(out, "-f", keyPath)
		}
	}
	return out
}

// BufferFile returns the file operand of a sign call (the last argument), or "".
func BufferFile(args []string) string {
	if len(args) == 0 {
		return ""
	}
	last := args[len(args)-1]
	if strings.HasPrefix(last, "-") {
		return ""
	}
	return last
}

// ErrIdentityMismatch means the object being signed names someone else.
var ErrIdentityMismatch = errors.New("identity mismatch")

// CheckIdentity reads a commit or tag payload and checks that its committer
// (or tagger) email is the agent's. Payloads that are neither are allowed.
func CheckIdentity(r io.Reader, email string) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break // end of headers
		}
		var who string
		switch {
		case strings.HasPrefix(line, "committer "):
			who = strings.TrimPrefix(line, "committer ")
		case strings.HasPrefix(line, "tagger "):
			who = strings.TrimPrefix(line, "tagger ")
		default:
			continue
		}
		got := identEmail(who)
		if !strings.EqualFold(got, email) {
			return fmt.Errorf("%w: object is by %q but agent email is %q", ErrIdentityMismatch, got, email)
		}
		return nil
	}
	return sc.Err()
}

// identEmail extracts the email from "Name <email> 1700000000 +0000".
func identEmail(ident string) string {
	start := strings.IndexByte(ident, '<')
	end := strings.IndexByte(ident, '>')
	if start < 0 || end < start {
		return ""
	}
	addr := ident[start+1 : end]
	if a, err := mail.ParseAddress("<" + addr + ">"); err == nil {
		return a.Address
	}
	return addr
}

// Run executes ssh-keygen with the given arguments and standard streams,
// returning its exit code.
func Run(sshKeygen string, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	cmd := exec.Command(sshKeygen, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// CheckBufferIdentity opens the buffer file of a sign call and runs
// CheckIdentity on it. A missing operand (data on stdin) is allowed.
func CheckBufferIdentity(args []string, email string) error {
	path := BufferFile(args)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return CheckIdentity(bytes.NewReader(data), email)
}

// Package credential implements the git credential helper protocol
// (https://git-scm.com/docs/git-credential#IOFMT).
package credential

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Request is the attribute set git sends on stdin.
type Request struct {
	Protocol string
	Host     string // may include :port
	Path     string // only sent when credential.useHttpPath is set
	Username string
	// Other holds attributes this package doesn't interpret, e.g. "capability[]".
	Other map[string][]string
}

// maxInput bounds what we read from git; real requests are tiny.
const maxInput = 64 << 10

// Parse reads attributes until a blank line or EOF. A url= attribute is
// expanded into protocol, host, path and username, as git itself does, and
// later explicit attributes override it.
func Parse(r io.Reader) (*Request, error) {
	req := &Request{Other: map[string][]string{}}
	sc := bufio.NewScanner(io.LimitReader(r, maxInput))
	sc.Buffer(make([]byte, 4096), maxInput)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" {
			break
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("malformed credential line %q", redact(line))
		}
		if strings.ContainsRune(val, 0) {
			return nil, fmt.Errorf("NUL in credential attribute %q", key)
		}
		switch key {
		case "protocol":
			req.Protocol = val
		case "host":
			req.Host = val
		case "path":
			req.Path = val
		case "username":
			req.Username = val
		case "url":
			if err := req.applyURL(val); err != nil {
				return nil, err
			}
		default:
			req.Other[key] = append(req.Other[key], val)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return req, nil
}

func (r *Request) applyURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("bad url attribute: %w", err)
	}
	r.Protocol = u.Scheme
	r.Host = u.Host
	if p := strings.TrimPrefix(u.Path, "/"); p != "" {
		r.Path = p
	}
	if u.User != nil && u.User.Username() != "" {
		r.Username = u.User.Username()
	}
	return nil
}

// Hostname returns Host without a port, lower-cased.
func (r *Request) Hostname() string {
	h := r.Host
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return strings.ToLower(h)
}

// Repo returns "owner/name" from Path, or "" if Path doesn't look like a
// GitHub-style repo path.
func (r *Request) Repo() string {
	p := strings.Trim(r.Path, "/")
	p = strings.TrimSuffix(p, ".git")
	parts := strings.Split(p, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// Response is what the helper prints for "get".
type Response struct {
	Username string
	Password string
	Expiry   *time.Time
}

// Write emits the response in git's format. Attributes are only written when
// set; unknown ones (password_expiry_utc on old git) are ignored by git.
func (resp *Response) Write(w io.Writer) error {
	var b strings.Builder
	if resp.Username != "" {
		fmt.Fprintf(&b, "username=%s\n", resp.Username)
	}
	if resp.Password != "" {
		fmt.Fprintf(&b, "password=%s\n", resp.Password)
	}
	if resp.Expiry != nil {
		fmt.Fprintf(&b, "password_expiry_utc=%d\n", resp.Expiry.Unix())
	}
	for _, v := range []string{resp.Username, resp.Password} {
		if strings.ContainsAny(v, "\n\x00") {
			return fmt.Errorf("credential value contains newline or NUL")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// redact hides values in error messages; credential input may contain secrets.
func redact(line string) string {
	if k, _, ok := strings.Cut(line, "="); ok {
		return k + "=<redacted>"
	}
	if len(line) > 16 {
		return line[:16] + "…"
	}
	return line
}

// SortedKeys is a helper for stable debug output of Other.
func (r *Request) SortedKeys() []string {
	keys := make([]string, 0, len(r.Other))
	for k := range r.Other {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

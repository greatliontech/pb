// Package netrc reads the credential file (module-resolution.md, the
// credential file term) and lends its credentials to the requests pb
// makes over HTTPS (REQ-resolve-credentials): the HTTP client's
// transport, for discovery and proxy fetches, and the git transport's
// credential source, for listings and fetches of an origin. Both
// answer by the request's host, over HTTPS alone — a credential goes
// to the host it names over a channel that hides it, or nowhere.
package netrc

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-git/go-git/v6/plumbing/client"
	xhttp "github.com/go-git/go-git/v6/plumbing/transport/http"
)

// ErrInvalid is wrapped when the file holds a token the grammar does
// not name.
var ErrInvalid = errors.New("invalid credential file")

// Credential is one entry's login and password.
type Credential struct {
	Login, Password string
}

// File is a parsed credential file: entries by host, in file order;
// the default entry, read for the grammar's sake, is kept apart and
// lent to no host.
type File struct {
	entries []*entry
	def     *entry
}

// entry is one machine or default entry, complete or not.
type entry struct {
	host            string
	login, password string
	hasLogin, hasPW bool
}

// DefaultPath is the credential file's default location: .netrc in
// the user's home directory, _netrc on Windows, as git and Go read it.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("netrc: resolving the home directory: %w", err)
	}
	return filepath.Join(home, defaultName(runtime.GOOS)), nil
}

// defaultName is the file's name on the platform.
func defaultName(goos string) string {
	if goos == "windows" {
		return "_netrc"
	}
	return ".netrc"
}

// Load reads and parses the file at path. An absent file is an empty
// one when absentIsEmpty — the default location, which no layer
// stated — and an error otherwise: a stated location that is absent
// is a slip, never a silently empty file.
func Load(path string, absentIsEmpty bool) (*File, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && absentIsEmpty {
		return &File{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("netrc: %w", err)
	}
	f, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}
	return f, nil
}

// Parse reads the netrc grammar: whitespace-separated tokens, a token
// quoted as curl quotes one — between double quotes, \" \\ \n \r
// and \t escaped — taken whole; machine and default opening entries,
// login, password and account filling the open one, macdef skipping a
// macro through the next blank line; a token the grammar does not
// name, a value the file ends before, or a quote the line ends
// before, is an error. A login, password or account before any entry
// opens is an error too: the grammar has no entry for them to fill.
// The default entry is read and never lent: pb reaches hosts the
// dependency graph names, not ones the user typed, and a credential
// for every host would go to any of them (the credential file term).
func Parse(data []byte) (*File, error) {
	f := &File{}
	var open *entry
	lines := bufio.NewScanner(bytes.NewReader(data))
	lines.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var tokens []string
	pos := 0
	line := 0
	inMacro := false
	var tokenErr error
	// next yields the next token, reading lines as needed; ok=false at
	// the end of the file. A macro body runs through the next blank
	// line and yields no token.
	next := func() (string, bool) {
		for {
			if pos < len(tokens) {
				t := tokens[pos]
				pos++
				return t, true
			}
			if !lines.Scan() {
				return "", false
			}
			line++
			text := lines.Text()
			if inMacro {
				if strings.TrimSpace(text) == "" {
					inMacro = false
				}
				continue
			}
			var err error
			if tokens, err = tokenize(text); err != nil {
				tokenErr = fmt.Errorf("line %d: %w", line, err)
				return "", false
			}
			pos = 0
		}
	}
	for {
		tok, ok := next()
		if !ok {
			break
		}
		value := func() (string, error) {
			v, ok := next()
			if !ok {
				if tokenErr != nil {
					return "", tokenErr
				}
				return "", fmt.Errorf("line %d: %s names no value", line, tok)
			}
			return v, nil
		}
		switch tok {
		case "machine":
			host, err := value()
			if err != nil {
				return nil, err
			}
			open = &entry{host: strings.ToLower(hostname(host))}
			f.entries = append(f.entries, open)
		case "default":
			f.def = &entry{}
			open = f.def
		case "login", "password", "account":
			v, err := value()
			if err != nil {
				return nil, err
			}
			if open == nil {
				return nil, fmt.Errorf("line %d: %s before any machine or default entry", line, tok)
			}
			switch tok {
			case "login":
				open.login, open.hasLogin = v, true
			case "password":
				open.password, open.hasPW = v, true
			}
		case "macdef":
			if _, err := value(); err != nil {
				return nil, err
			}
			inMacro = true
			// The macro's body starts on the next line: the rest of
			// this one belongs to the definition line.
			tokens, pos = nil, 0
		default:
			return nil, fmt.Errorf("line %d: unknown token %q", line, tok)
		}
	}
	if tokenErr != nil {
		return nil, tokenErr
	}
	if err := lines.Err(); err != nil {
		return nil, err
	}
	return f, nil
}

// tokenize splits one line into its tokens: runs of non-space bytes,
// or a quoted token between double quotes with curl's escapes, which
// the line must close.
func tokenize(text string) ([]string, error) {
	var tokens []string
	i := 0
	for i < len(text) {
		switch c := text[i]; {
		case c == ' ' || c == '\t':
			i++
		case c == '"':
			var b strings.Builder
			j := i + 1
			for {
				if j >= len(text) {
					return nil, fmt.Errorf("a quoted token the line does not close")
				}
				if text[j] == '"' {
					break
				}
				if text[j] == '\\' && j+1 < len(text) {
					j++
					switch text[j] {
					case 'n':
						b.WriteByte('\n')
					case 'r':
						b.WriteByte('\r')
					case 't':
						b.WriteByte('\t')
					default:
						b.WriteByte(text[j])
					}
					j++
					continue
				}
				b.WriteByte(text[j])
				j++
			}
			tokens = append(tokens, b.String())
			i = j + 1
		default:
			j := i
			for j < len(text) && text[j] != ' ' && text[j] != '\t' {
				j++
			}
			tokens = append(tokens, text[i:j])
			i = j
		}
	}
	return tokens, nil
}

// Lookup is the credential for a host: the first machine entry naming
// it — by name alone, case-insensitively, any port aside — holding a
// login and a password; the default entry is lent to no host.
func (f *File) Lookup(host string) (Credential, bool) {
	if f == nil {
		return Credential{}, false
	}
	name := strings.ToLower(hostname(host))
	for _, e := range f.entries {
		if e.host == name {
			return e.credential()
		}
	}
	return Credential{}, false
}

// credential is the entry as a credential, where complete.
func (e *entry) credential() (Credential, bool) {
	if !e.hasLogin || !e.hasPW {
		return Credential{}, false
	}
	return Credential{Login: e.login, Password: e.password}, true
}

// hostname strips a port from a host: a bracketed IPv6 host keeps its
// brackets' content, any other its part before the last colon.
func hostname(host string) string {
	if strings.HasPrefix(host, "[") {
		if i := strings.IndexByte(host, ']'); i > 0 {
			return host[1:i]
		}
		return host
	}
	if i := strings.LastIndexByte(host, ':'); i >= 0 && strings.Count(host, ":") == 1 {
		return host[:i]
	}
	return host
}

// RoundTripper wraps base so that every HTTPS request to a host the
// file holds a credential for carries it as basic authorization, the
// request's own authorization header aside; a request over any other
// scheme goes as it is. Requests are never mutated: a credentialed one
// is a clone.
func (f *File) RoundTripper(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTripper{base: base, file: f}
}

type roundTripper struct {
	base http.RoundTripper
	file *File
}

func (rt roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.Header.Get("Authorization") != "" {
		return rt.base.RoundTrip(req)
	}
	c, ok := rt.file.Lookup(req.URL.Host)
	if !ok {
		return rt.base.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	req.SetBasicAuth(c.Login, c.Password)
	return rt.base.RoundTrip(req)
}

// ClientOptions is the git transport's credential source over the
// file: consulted for the repository's origin and each redirect
// target by host, answering over HTTPS alone.
func (f *File) ClientOptions() []client.Option {
	return []client.Option{client.WithHTTPCredentials(func(_ context.Context, req *client.CredentialRequest) (*client.Credential, error) {
		if req.TargetOrigin.Scheme != "https" {
			return nil, nil
		}
		c, ok := f.Lookup(req.TargetOrigin.Host)
		if !ok {
			return nil, nil
		}
		return &client.Credential{Authorizer: (&xhttp.BasicAuth{Username: c.Login, Password: c.Password}).Authorizer}, nil
	})}
}

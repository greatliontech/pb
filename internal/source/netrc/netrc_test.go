package netrc

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// The grammar (module-resolution.md, the credential file term):
// entries by machine and default, login and password filling the open
// one, account accepted and unused, a macro skipped through its blank
// line, a quoted token taken whole with curl's escapes; a host matched
// by name alone, case-insensitively, any port aside, a bracketed IPv6
// literal by its address, the first entry winning; an entry lacking a
// login or a password is no credential, and the default entry is lent
// to no host.
func TestParseAndLookup(t *testing.T) {
	f, err := Parse([]byte(`
machine Corp.Example.com login alice password s3cret
machine corp.example.com login bob password later
machine api.example.com
  login carol
  account unused
  password "a b\"c\\d\te"
macdef init
  machine inside.example login x password y
  get README

machine half.example.com login dave
machine [::1] login six password v6
default login guest password anon
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		host string
		want Credential
		ok   bool
	}{
		{"corp.example.com", Credential{"alice", "s3cret"}, true},
		{"CORP.EXAMPLE.COM:8443", Credential{"alice", "s3cret"}, true},
		{"api.example.com", Credential{"carol", "a b\"c\\d\te"}, true},
		{"half.example.com", Credential{}, false},
		{"inside.example", Credential{}, false},
		{"other.example.com", Credential{}, false},
		{"[::1]:8443", Credential{"six", "v6"}, true},
	} {
		got, ok := f.Lookup(tc.host)
		if ok != tc.ok || got != tc.want {
			t.Errorf("Lookup(%q) = %+v, %v; want %+v, %v", tc.host, got, ok, tc.want, tc.ok)
		}
	}
	if f.def == nil || f.def.login != "guest" {
		t.Fatalf("the default entry was not read: %+v", f.def)
	}
	// A quoted token stands alone: `"secret"` is secret, not "secret".
	g, _ := Parse([]byte("machine a.example login x password \"secret\"\n"))
	if c, ok := g.Lookup("a.example"); !ok || c.Password != "secret" {
		t.Fatalf("a quoted password: %+v %v", c, ok)
	}
	if _, ok := (*File)(nil).Lookup("a.example"); ok {
		t.Fatal("a nil file lent a credential")
	}
	// An empty file is empty.
	if e, err := Parse(nil); err != nil || len(e.entries) != 0 || e.def != nil {
		t.Fatalf("empty file: %+v, %v", e, err)
	}
}

// A token the grammar does not name, a value the file ends before, and
// a field before any entry are each refused naming the line.
func TestParseRefusals(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"machine a.example port 22\n", `line 1: unknown token "port"`},
		{"machine a.example\nlogin\n", "line 2: login names no value"},
		{"login x password y\n", "line 1: login before any machine or default entry"},
		{"machine\n", "line 1: machine names no value"},
		{"machine a.example login u password \"a b\n", "line 1: a quoted token the line does not close"},
		{"machine a.example login u password\n\"abc\n", "line 2: a quoted token the line does not close"}, // the quote fails where a value is awaited
		{"macdef\n", "line 1: macdef names no value"},
	} {
		_, err := Parse([]byte(tc.in))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Parse(%q) = %v, want %q", tc.in, err, tc.want)
		}
	}
}

// For any set of complete entries the lookup of a host is the first
// entry naming it, however the host's letters are cased and whatever
// port rides along, and a host no entry names has none, a default
// entry or not.
func TestLookupProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(rt, "n")
		var b strings.Builder
		type ent struct{ host, login, pw string }
		var ents []ent
		for i := range n {
			e := ent{
				host:  fmt.Sprintf("h%d.example", rapid.IntRange(0, 3).Draw(rt, "host")),
				login: fmt.Sprintf("l%d", i),
				pw:    fmt.Sprintf("p%d", i),
			}
			ents = append(ents, e)
			fmt.Fprintf(&b, "machine %s login %s password %s\n", e.host, e.login, e.pw)
		}
		withDefault := rapid.Bool().Draw(rt, "default")
		if withDefault {
			b.WriteString("default login d password dp\n")
		}
		f, err := Parse([]byte(b.String()))
		if err != nil {
			rt.Fatal(err)
		}
		host := fmt.Sprintf("h%d.example", rapid.IntRange(0, 4).Draw(rt, "lookup"))
		spelled := host
		if rapid.Bool().Draw(rt, "upper") {
			spelled = strings.ToUpper(spelled)
		}
		if rapid.Bool().Draw(rt, "port") {
			spelled += ":443"
		}
		got, ok := f.Lookup(spelled)
		var want Credential
		wantOK := false
		for _, e := range ents {
			if e.host == host {
				want, wantOK = Credential{e.login, e.pw}, true
				break
			}
		}
		if ok != wantOK || got != want {
			rt.Fatalf("Lookup(%q) = %+v, %v; want %+v, %v", spelled, got, ok, want, wantOK)
		}
	})
}

// Load: the default location absent is an empty file; a stated
// location absent, unreadable or malformed is an error naming the
// path; the default name is .netrc, _netrc on Windows.
func TestLoad(t *testing.T) {
	dir := t.TempDir()
	absent := filepath.Join(dir, "none")
	if f, err := Load(absent, true); err != nil || f == nil {
		t.Fatalf("absent default: %v, %v", f, err)
	}
	if _, err := Load(absent, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent stated: %v", err)
	}
	bad := filepath.Join(dir, "bad")
	os.WriteFile(bad, []byte("machine a.example port 1\n"), 0o600)
	if _, err := Load(bad, true); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), bad) {
		t.Fatalf("malformed: %v", err)
	}
	good := filepath.Join(dir, "good")
	os.WriteFile(good, []byte("machine a.example login u password p\n"), 0o600)
	f, err := Load(good, false)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := f.Lookup("a.example"); !ok || c.Login != "u" {
		t.Fatalf("loaded: %+v %v", c, ok)
	}
	if defaultName("windows") != "_netrc" || defaultName("linux") != ".netrc" || defaultName("darwin") != ".netrc" {
		t.Fatal("the default name drifted from git's and Go's")
	}
}

// The HTTP transport lends a host's credential to HTTPS requests alone
// (REQ-resolve-credentials): a cleartext request to the same host
// carries none, an unnamed host none, a request already authorized
// keeps its own, and the caller's request is never mutated.
func TestRoundTripper(t *testing.T) {
	seen := ""
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	f, _ := Parse([]byte("machine 127.0.0.1 login u password p\n"))
	base := srv.Client().Transport.(*http.Transport)
	c := &http.Client{Transport: f.RoundTripper(base)}
	want := "Basic " + basic("u", "p")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/x", nil)
	if _, err := c.Do(req); err != nil || seen != want {
		t.Fatalf("https: seen %q, err %v", seen, err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("the caller's request was mutated")
	}

	seen = "unset"
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/x", nil)
	req.SetBasicAuth("own", "header")
	if _, err := c.Do(req); err != nil || seen != "Basic "+basic("own", "header") {
		t.Fatalf("own authorization: seen %q, err %v", seen, err)
	}

	// Cleartext to the same host: the transport is dialed with a plain
	// URL that the TLS server refuses, so the credential's absence is
	// observed at the round tripper itself.
	rec := &recording{}
	c = &http.Client{Transport: f.RoundTripper(rec)}
	for _, u := range []string{"http://" + host + "/x", "https://other.example/x"} {
		req, _ = http.NewRequest(http.MethodGet, u, nil)
		c.Do(req)
		if rec.last.Header.Get("Authorization") != "" {
			t.Fatalf("%s carried a credential", u)
		}
	}
	req, _ = http.NewRequest(http.MethodGet, "https://127.0.0.1:1/x", nil)
	c.Do(req)
	if rec.last.Header.Get("Authorization") != want {
		t.Fatalf("https by host: %q", rec.last.Header.Get("Authorization"))
	}
	// A nil base is the default transport.
	if f.RoundTripper(nil).(roundTripper).base != http.DefaultTransport {
		t.Fatal("nil base")
	}
}

// A redirect's target is authorized by its own entry: the first host's
// credential never reaches the second, and the second host's own does
// — the two servers on one loopback address, reached by two names
// with an entry each.
func TestRoundTripperRedirectByHost(t *testing.T) {
	var first, second string
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		second = r.Header.Get("Authorization")
	}))
	defer target.Close()
	tu, _ := url.Parse(target.URL)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first = r.Header.Get("Authorization")
		http.Redirect(w, r, "https://localhost:"+tu.Port()+"/there", http.StatusFound)
	}))
	defer origin.Close()
	f, _ := Parse([]byte("machine 127.0.0.1 login a password 1\nmachine localhost login b password 2\n"))
	base := origin.Client().Transport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // the test certificate names no localhost
	c := &http.Client{Transport: f.RoundTripper(base)}
	if _, err := c.Get(origin.URL + "/x"); err != nil {
		t.Fatal(err)
	}
	if first != "Basic "+basic("a", "1") || second != "Basic "+basic("b", "2") {
		t.Fatalf("the first host saw %q, the redirect target %q", first, second)
	}
}

func basic(u, p string) string {
	req, _ := http.NewRequest(http.MethodGet, "https://x/", nil)
	req.SetBasicAuth(u, p)
	return strings.TrimPrefix(req.Header.Get("Authorization"), "Basic ")
}

// recording keeps the last request it was handed and answers nothing.
type recording struct{ last *http.Request }

func (r *recording) RoundTrip(req *http.Request) (*http.Response, error) {
	r.last = req
	return nil, errors.New("recorded")
}

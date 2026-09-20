package origin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/greatliontech/pb/internal/module/version"
)

func TestSplitVCSGolden(t *testing.T) {
	cases := map[string]*Origin{
		"example.com/repo.git":             {Repo: "https://example.com/repo.git", Subtree: ""},
		"example.com/repo.git/sub/tree":    {Repo: "https://example.com/repo.git", Subtree: "sub/tree"},
		"example.com/a/b.git/protos/v1":    {Repo: "https://example.com/a/b.git", Subtree: "protos/v1"},
		"example.com/first.git/second.git": {Repo: "https://example.com/first.git", Subtree: "second.git"},
		"example.com/repo":                 nil,
		"example.com/repo/sub":             nil,
		"example.com/git/sub":              nil, // segment "git" does not end in ".git"
		"example.com/.git/sub":             nil, // bare ".git" is not a repository segment
	}
	for path, want := range cases {
		got, ok := SplitVCS(path)
		if want == nil {
			if ok {
				t.Errorf("SplitVCS(%q) = %+v, want no split", path, got)
			}
			continue
		}
		if !ok || got != *want {
			t.Errorf("SplitVCS(%q) = %+v ok=%v, want %+v", path, got, ok, want)
		}
	}
}

// The bare host is never a probe candidate: a host answering listings at
// its root must not capture every module path on it.
func TestPrefixesGolden(t *testing.T) {
	got := prefixes("example.com/a/b/c")
	want := []string{"example.com/a", "example.com/a/b", "example.com/a/b/c"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("prefixes = %v, want %v", got, want)
	}
}

func TestSubtreeOfGolden(t *testing.T) {
	if sub, ok := subtreeOf("example.com/x", "example.com/x"); !ok || sub != "" {
		t.Errorf("exact match = (%q, %v), want (\"\", true)", sub, ok)
	}
	// Failure returns the zero value, not a partial remainder.
	if sub, ok := subtreeOf("example.com/x/y", "example.com/z"); ok || sub != "" {
		t.Errorf("mismatch = (%q, %v), want (\"\", false)", sub, ok)
	}
}

// Prefix matching is segment-exact: a prefix never matches inside a
// segment, and the subtree is exactly the remainder.
func TestSubtreeOfProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seg := func(label string) string {
			return string(rapid.SliceOfN(rapid.SampledFrom([]rune("abc12")), 1, 3).Draw(t, label))
		}
		n := rapid.IntRange(1, 4).Draw(t, "n")
		segs := make([]string, n+1)
		segs[0] = seg("host") + ".com"
		for i := 1; i <= n; i++ {
			segs[i] = seg(fmt.Sprint("s", i))
		}
		path := strings.Join(segs, "/")
		cut := rapid.IntRange(1, n+1).Draw(t, "cut")
		prefix := strings.Join(segs[:cut], "/")
		sub, ok := subtreeOf(path, prefix)
		if !ok {
			t.Fatalf("segment prefix %q rejected for %q", prefix, path)
		}
		if want := strings.Join(segs[cut:], "/"); sub != want {
			t.Fatalf("subtreeOf(%q, %q) = %q, want %q", path, prefix, sub, want)
		}
		// Dropping the prefix's final character never matches: the chop
		// lands mid-segment (or leaves a trailing slash), and neither is a
		// segment-exact prefix of the path.
		chopped := prefix[:len(prefix)-1]
		if sub, ok := subtreeOf(path, chopped); ok {
			t.Fatalf("chopped prefix %q matched %q with subtree %q", chopped, path, sub)
		}
	})
}

func refs(t *testing.T, pairs ...string) []Ref {
	t.Helper()
	if len(pairs)%2 != 0 {
		t.Fatal("refs wants name/hash pairs")
	}
	out := make([]Ref, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, Ref{Name: pairs[i], Hash: pairs[i+1]})
	}
	return out
}

func TestReleaseTagsGolden(t *testing.T) {
	rs := refs(t,
		"refs/heads/main", "h0",
		"refs/tags/3.0.0", "t0", // invalid (no v) early: later valid tags must survive it
		"refs/tags/v1.0.0", "t1",
		"refs/tags/v1.0.0^{}", "c1",
		"refs/tags/v2.0.0", "t2",
		"refs/tags/not-a-version", "t3",
		"refs/tags/protos/v1/v1.5.0", "t4",
		"refs/tags/protos/v0.9.0", "t5",
		"refs/tags/protosx/v3.0.0", "t6",
		"refs/tags/v0.5.0", "t7", // valid after the invalid ones
		// Pseudo-version-shaped: parses as a canonical version but is
		// never a release tag (REQ-resolve-pseudo-base) — resolution
		// binds a pseudo-version to the commit its hash embeds, not to
		// a tag, so listing it would advertise an unresolvable version.
		"refs/tags/v3.0.1-0.20260101000000-aaaaaaaaaaaa", "t8",
		// A bare version-string ref name: origin-controlled bytes that
		// parse as a version but sit outside refs/tags/, which is the
		// only home of a release (REQ-resolve-release-tags). Only the
		// prefix guard excludes it — version syntax alone would not.
		"v9.9.9", "t9",
	)
	root := ReleaseTags(rs, "")
	if len(root) != 3 || root[0].Version.String() != "v0.5.0" ||
		root[1].Version.String() != "v1.0.0" || root[2].Version.String() != "v2.0.0" {
		t.Fatalf("root tags = %+v", root)
	}
	if root[0].Hash != "t7" || root[1].Hash != "t1" || root[1].Peeled != "c1" || root[2].Peeled != "" {
		t.Fatalf("root tag hashes = %+v", root)
	}
	// Subtree named protos: segment-exact — protosx and protos/v1 tags are
	// other modules' releases.
	sub := ReleaseTags(rs, "protos")
	if len(sub) != 1 || sub[0].Version.String() != "v0.9.0" || sub[0].Hash != "t5" {
		t.Fatalf("protos tags = %+v", sub)
	}
	// A subtree whose own name is a version segment works verbatim: the
	// module path carries schema-idiom version directories, never module
	// identity (REQ-resolve-no-import-versioning).
	v1 := ReleaseTags(rs, "protos/v1")
	if len(v1) != 1 || v1[0].Version.String() != "v1.5.0" || v1[0].Hash != "t4" {
		t.Fatalf("protos/v1 tags = %+v", v1)
	}
}

// Output is sorted by version and independent of listing order.
func TestReleaseTagsDeterminismProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		versions := []string{"v1.0.0", "v1.2.0", "v2.0.0", "v0.1.0", "v1.0.0-rc.1"}
		n := rapid.IntRange(0, 5).Draw(t, "n")
		var rs []Ref
		for i := range n {
			rs = append(rs, Ref{Name: "refs/tags/" + versions[i], Hash: fmt.Sprint("h", i)})
		}
		shuffled := append([]Ref(nil), rs...)
		for i := len(shuffled) - 1; i > 0; i-- {
			j := rapid.IntRange(0, i).Draw(t, fmt.Sprint("j", i))
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		}
		a, b := ReleaseTags(rs, ""), ReleaseTags(shuffled, "")
		if fmt.Sprintf("%+v", a) != fmt.Sprintf("%+v", b) {
			t.Fatalf("listing order leaked:\n%+v\n%+v", a, b)
		}
		for i := 1; i < len(a); i++ {
			if version.Compare(a[i-1].Version, a[i].Version) >= 0 {
				t.Fatalf("not strictly ascending: %+v", a)
			}
		}
	})
}

// fakeProber records every List call and answers per its script.
type fakeProber struct {
	calls  []string
	answer map[string][]Ref
	errs   map[string]error
}

func (f *fakeProber) List(_ context.Context, repoURL string) ([]Ref, error) {
	f.calls = append(f.calls, repoURL)
	if err, ok := f.errs[repoURL]; ok {
		return nil, err
	}
	if rs, ok := f.answer[repoURL]; ok {
		return rs, nil
	}
	return nil, errors.New("no such repository")
}

// pageRT serves canned discovery responses fully in-process — no
// listener, no goroutines, so the oracle is deterministic. Keyed by
// host+path; redirects maps a key to a Location header served as 302,
// statuses overrides a page's 200, errs fails the round trip outright.
// Every round-tripped URL is recorded for scheme assertions, and every
// served body tracks whether it was closed.
type pageRT struct {
	pages     map[string]string
	redirects map[string]string
	statuses  map[string]int
	errs      map[string]error
	setCookie map[string]string
	requested []string
	cookies   []string
	bodies    []*trackedBody
}

func (rt *pageRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	rt.requested = append(rt.requested, req.URL.String())
	for _, c := range req.Cookies() {
		rt.cookies = append(rt.cookies, c.String())
	}
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("cleartext request %q reached the transport", req.URL)
	}
	key := req.URL.Host + req.URL.Path
	if err, ok := rt.errs[key]; ok {
		return nil, err
	}
	if loc, ok := rt.redirects[key]; ok {
		return rt.response(req, http.StatusFound, http.Header{"Location": {loc}}, ""), nil
	}
	page, ok := rt.pages[key]
	if !ok || req.URL.Query().Get("pb-get") != "1" {
		return rt.response(req, http.StatusNotFound, http.Header{}, "not found"), nil
	}
	status := http.StatusOK
	if s, ok := rt.statuses[key]; ok {
		status = s
	}
	header := http.Header{}
	if c, ok := rt.setCookie[key]; ok {
		header.Set("Set-Cookie", c)
	}
	return rt.response(req, status, header, page), nil
}

func (rt *pageRT) response(req *http.Request, status int, header http.Header, body string) *http.Response {
	b := &trackedBody{Reader: strings.NewReader(body)}
	rt.bodies = append(rt.bodies, b)
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       b,
		Request:    req,
	}
}

// trackedBody records whether a response body was closed.
type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

func assertBodiesClosed(t *testing.T, rt *pageRT) {
	t.Helper()
	for i, b := range rt.bodies {
		if !b.closed {
			t.Errorf("response body %d never closed", i)
		}
	}
}

// vanityClient serves canned HTML for https://<path>?pb-get=1 requests.
func vanityClient(t *testing.T, pages map[string]string) *http.Client {
	t.Helper()
	return &http.Client{Transport: &pageRT{pages: pages}}
}

func metaTag(prefix, repo string) string {
	return fmt.Sprintf(`<meta name="pb-import" content="%s git %s">`, prefix, repo)
}

func meta(prefix, repo string) string {
	return `<!doctype html><html><head>` + metaTag(prefix, repo) + `</head></html>`
}

func TestResolveVCSSuffixPrecedence(t *testing.T) {
	f := &fakeProber{}
	o, err := Resolve(context.Background(), Deps{Prober: f, Client: vanityClient(t, nil)}, "example.com/r.git/sub")
	if err != nil || o.Repo != "https://example.com/r.git" || o.Subtree != "sub" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("vcs-suffix path probed: %v", f.calls)
	}
}

func TestResolveVanityPrecedence(t *testing.T) {
	pages := map[string]string{
		"example.com/protos/api": meta("example.com/protos", "https://git.example.com/protos"),
	}
	f := &fakeProber{}
	o, err := Resolve(context.Background(), Deps{Prober: f, Client: vanityClient(t, pages)}, "example.com/protos/api")
	if err != nil || o.Repo != "https://git.example.com/protos" || o.Subtree != "api" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	// Vanity precedence: probing never consulted.
	if len(f.calls) != 0 {
		t.Fatalf("vanity-resolved path probed: %v", f.calls)
	}
}

func TestResolveVanityLongestPrefixWins(t *testing.T) {
	shorter := metaTag("example.com", "https://git.example.com/all")
	longer := metaTag("example.com/protos", "https://git.example.com/protos")
	for name, body := range map[string]string{
		"shorter first": shorter + longer,
		"longer first":  longer + shorter,
	} {
		t.Run(name, func(t *testing.T) {
			pages := map[string]string{"example.com/protos/api": `<html><head>` + body + `</head></html>`}
			o, err := Resolve(context.Background(), Deps{Prober: &fakeProber{}, Client: vanityClient(t, pages)}, "example.com/protos/api")
			if err != nil || o.Repo != "https://git.example.com/protos" || o.Subtree != "api" {
				t.Fatalf("o=%+v err=%v", o, err)
			}
		})
	}
}

// Duplicate declarations for the same prefix: the first in document
// order wins, deterministically.
func TestResolveVanityDuplicatePrefixFirstWins(t *testing.T) {
	page := `<html><head>` + metaTag("example.com/protos", "https://git.example.com/first") +
		metaTag("example.com/protos", "https://git.example.com/second") + `</head></html>`
	pages := map[string]string{"example.com/protos/api": page}
	o, err := Resolve(context.Background(), Deps{Prober: &fakeProber{}, Client: vanityClient(t, pages)}, "example.com/protos/api")
	if err != nil || o.Repo != "https://git.example.com/first" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
}

func TestResolveVanityRejectsNonHTTPS(t *testing.T) {
	pages := map[string]string{
		"example.com/protos/api": meta("example.com/protos", "ssh://git@example.com/protos"),
	}
	_, err := Resolve(context.Background(), Deps{Prober: &fakeProber{}, Client: vanityClient(t, pages)}, "example.com/protos/api")
	if err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("err = %v, want non-HTTPS rejection", err)
	}
}

// Declarations for prefixes that do not segment-exactly match fall
// through to probing rather than redirecting.
func TestResolveVanityIgnoresForeignPrefixes(t *testing.T) {
	pages := map[string]string{
		"example.com/protos/api": `<html><head>` + metaTag("example.com/proto", "https://git.example.com/x") +
			metaTag("other.com", "https://git.example.com/y") + `</head></html>`,
	}
	f := &fakeProber{answer: map[string][]Ref{"https://example.com/protos": nil}}
	o, err := Resolve(context.Background(), Deps{Prober: f, Client: vanityClient(t, pages)}, "example.com/protos/api")
	if err != nil || o.Repo != "https://example.com/protos" || o.Subtree != "api" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
}

// A foreign-prefix declaration before a matching one must not stop the
// scan: every declaration is considered.
func TestResolveVanityMatchAfterForeignPrefix(t *testing.T) {
	page := `<html><head>` + metaTag("other.com", "https://git.example.com/y") +
		metaTag("example.com/protos", "https://git.example.com/protos") + `</head></html>`
	pages := map[string]string{"example.com/protos/api": page}
	o, err := Resolve(context.Background(), Deps{Prober: &fakeProber{}, Client: vanityClient(t, pages)}, "example.com/protos/api")
	if err != nil || o.Repo != "https://git.example.com/protos" || o.Subtree != "api" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
}

// Only well-formed pb-import meta declarations in the head count: other
// meta names, non-git VCS markers, wrong field counts, and non-meta
// elements carrying the same attributes are all ignored, as is a
// comment that happens to read "head".
func TestResolveVanityIgnoresNonPBDeclarations(t *testing.T) {
	page := `<!doctype html><html><!--head--><head>` +
		`<meta name="go-import" content="example.com/protos git https://attacker.example.com/go">` +
		`<link name="pb-import" content="example.com/protos git https://attacker.example.com/link">` +
		`<meta name="pb-import" content="example.com/protos svn https://attacker.example.com/svn">` +
		`<meta name="pb-import" content="example.com/protos git">` +
		metaTag("example.com/protos", "https://git.example.com/protos") +
		`</head></html>`
	pages := map[string]string{"example.com/protos/api": page}
	o, err := Resolve(context.Background(), Deps{Prober: &fakeProber{}, Client: vanityClient(t, pages)}, "example.com/protos/api")
	if err != nil || o.Repo != "https://git.example.com/protos" || o.Subtree != "api" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
}

// Discovery follows at most 10 redirect hops; a longer chain is a
// failed request and falls through to probing.
func TestResolveVanityRedirectCapAtTen(t *testing.T) {
	redirects := map[string]string{"example.com/protos/api": "https://h1.example.com/x?pb-get=1"}
	for i := 1; i < 15; i++ {
		redirects[fmt.Sprintf("h%d.example.com/x", i)] = fmt.Sprintf("https://h%d.example.com/x?pb-get=1", i+1)
	}
	rt := &pageRT{redirects: redirects}
	f := &fakeProber{answer: map[string][]Ref{"https://example.com/protos": nil}}
	o, err := Resolve(context.Background(), Deps{Prober: f, Client: &http.Client{Transport: rt}}, "example.com/protos/api")
	if err != nil || o.Repo != "https://example.com/protos" || o.Subtree != "api" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	if len(rt.requested) != 10 {
		t.Fatalf("issued %d discovery requests, want exactly 10 (initial + 9 follows)", len(rt.requested))
	}
	assertBodiesClosed(t, rt)
}

// Non-2xx responses are absence even when their body carries a matching
// declaration; the 2xx boundary is exact on both edges.
func TestResolveVanityStatusBoundaries(t *testing.T) {
	for status, wantVanity := range map[int]bool{
		150: false,
		299: true,
		300: false,
		503: false,
	} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			rt := &pageRT{
				pages:    map[string]string{"example.com/protos/api": meta("example.com/protos", "https://git.example.com/protos")},
				statuses: map[string]int{"example.com/protos/api": status},
			}
			f := &fakeProber{answer: map[string][]Ref{"https://example.com/protos": nil}}
			o, err := Resolve(context.Background(), Deps{Prober: f, Client: &http.Client{Transport: rt}}, "example.com/protos/api")
			if err != nil {
				t.Fatalf("err=%v", err)
			}
			want := "https://example.com/protos"
			if wantVanity {
				want = "https://git.example.com/protos"
			}
			if o.Repo != want {
				t.Fatalf("status %d: repo = %q, want %q", status, o.Repo, want)
			}
			assertBodiesClosed(t, rt)
		})
	}
}

// Discovery is cookie-free: the caller's jar is neither presented to
// module hosts nor seeded by discovery responses.
func TestResolveVanityIgnoresCookieJar(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	u := &url.URL{Scheme: "https", Host: "example.com", Path: "/protos/api"}
	jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: "secret"}})
	rt := &pageRT{
		pages:     map[string]string{"example.com/protos/api": meta("example.com/protos", "https://git.example.com/protos")},
		setCookie: map[string]string{"example.com/protos/api": "planted=by-host; Path=/"},
	}
	client := &http.Client{Transport: rt, Jar: jar}
	o, err := Resolve(context.Background(), Deps{Prober: &fakeProber{}, Client: client}, "example.com/protos/api")
	if err != nil || o.Repo != "https://git.example.com/protos" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	if got := rt.cookies; len(got) != 0 {
		t.Fatalf("discovery presented caller cookies: %v", got)
	}
	after := jar.Cookies(u)
	if len(after) != 1 || after[0].Name != "session" {
		t.Fatalf("discovery response seeded the caller's jar: %v", after)
	}
}

// A transport-level discovery failure with a live context is absence:
// resolution falls through to probing (REQ-resolve-vanity's failed
// request clause).
func TestResolveVanityTransportErrorFallsThrough(t *testing.T) {
	rt := &pageRT{errs: map[string]error{"example.com/protos/api": errors.New("connection refused")}}
	f := &fakeProber{answer: map[string][]Ref{"https://example.com/protos": nil}}
	o, err := Resolve(context.Background(), Deps{Prober: f, Client: &http.Client{Transport: rt}}, "example.com/protos/api")
	if err != nil || o.Repo != "https://example.com/protos" || o.Subtree != "api" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
}

func TestResolveProbingOrderAndFirstAnswer(t *testing.T) {
	f := &fakeProber{answer: map[string][]Ref{
		"https://example.com/a/b":   nil,
		"https://example.com/a/b/c": nil, // longer prefix also answers; first wins
	}}
	o, err := Resolve(context.Background(), Deps{Prober: f, Client: vanityClient(t, nil)}, "example.com/a/b/c/d")
	if err != nil || o.Repo != "https://example.com/a/b" || o.Subtree != "c/d" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	want := []string{"https://example.com/a", "https://example.com/a/b"}
	if fmt.Sprint(f.calls) != fmt.Sprint(want) {
		t.Fatalf("probe order = %v, want %v", f.calls, want)
	}
}

func TestResolveNoOrigin(t *testing.T) {
	f := &fakeProber{}
	_, err := Resolve(context.Background(), Deps{Prober: f, Client: vanityClient(t, nil)}, "example.com/a/b")
	if !errors.Is(err, ErrNoOrigin) {
		t.Fatalf("err = %v, want ErrNoOrigin", err)
	}
	for _, part := range []string{
		"example.com/a/b",
		"no vanity redirect and no prefix answered a reference listing",
		"https://example.com/a: no such repository; https://example.com/a/b: no such repository",
	} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q does not name %q", err, part)
		}
	}
}

// An invalid path is rejected before any network effect: neither the
// vanity client nor the prober is ever consulted.
func TestResolveRejectsInvalidPath(t *testing.T) {
	rt := &pageRT{}
	f := &fakeProber{}
	_, err := Resolve(context.Background(), Deps{Prober: f, Client: &http.Client{Transport: rt}}, "nodot/x")
	if err == nil {
		t.Fatal("invalid path accepted")
	}
	if len(f.calls) != 0 || len(rt.requested) != 0 {
		t.Fatalf("invalid path reached the network: probes=%v requests=%v", f.calls, rt.requested)
	}
}

// A discovery redirect to a cleartext URL aborts the request — nothing
// is fetched over HTTP — and discovery falls through to probing.
func TestResolveVanityRedirectToCleartextFallsThrough(t *testing.T) {
	rt := &pageRT{
		pages:     map[string]string{"evil.example.com/x": meta("example.com", "https://attacker.example.com/all")},
		redirects: map[string]string{"example.com/protos/api": "http://evil.example.com/x?pb-get=1"},
	}
	f := &fakeProber{answer: map[string][]Ref{"https://example.com/protos": nil}}
	o, err := Resolve(context.Background(), Deps{Prober: f, Client: &http.Client{Transport: rt}}, "example.com/protos/api")
	if err != nil || o.Repo != "https://example.com/protos" || o.Subtree != "api" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	for _, u := range rt.requested {
		if !strings.HasPrefix(u, "https://") {
			t.Fatalf("cleartext URL requested: %q", u)
		}
	}
}

// HTTPS-to-HTTPS redirects are followed: vanity hosting behind a
// canonical-host redirect still resolves.
func TestResolveVanityFollowsHTTPSRedirect(t *testing.T) {
	rt := &pageRT{
		pages:     map[string]string{"www.example.com/protos/api": meta("example.com/protos", "https://git.example.com/protos")},
		redirects: map[string]string{"example.com/protos/api": "https://www.example.com/protos/api?pb-get=1"},
	}
	o, err := Resolve(context.Background(), Deps{Prober: &fakeProber{}, Client: &http.Client{Transport: rt}}, "example.com/protos/api")
	if err != nil || o.Repo != "https://git.example.com/protos" || o.Subtree != "api" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
}

// A canceled context during discovery is an error, never absence: a
// repository that exists must not be reported as ErrNoOrigin because the
// caller gave up.
func TestResolveVanityCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Resolve(ctx, Deps{Prober: &fakeProber{}, Client: vanityClient(t, nil)}, "example.com/a/b")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrNoOrigin) {
		t.Fatalf("cancellation misreported as ErrNoOrigin: %v", err)
	}
}

// Cancellation mid-probe is likewise an error, not ErrNoOrigin.
func TestResolveProbingCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &cancelingProber{cancel: cancel}
	_, err := Resolve(ctx, Deps{Prober: f, Client: vanityClient(t, nil)}, "example.com/a/b")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrNoOrigin) {
		t.Fatalf("cancellation misreported as ErrNoOrigin: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("probing continued after cancellation: %d calls", f.calls)
	}
}

// cancelingProber cancels the resolution context on its first List call
// and fails it, as a transport would once its context dies.
type cancelingProber struct {
	cancel context.CancelFunc
	calls  int
}

func (p *cancelingProber) List(ctx context.Context, _ string) ([]Ref, error) {
	p.calls++
	p.cancel()
	return nil, ctx.Err()
}

// Discovery reads at most maxDiscoveryBody bytes: a declaration past the
// limit is absent, so an attacker-sized body cannot exhaust memory and
// resolution falls through to probing.
func TestResolveVanityBodyBeyondLimitIgnored(t *testing.T) {
	page := strings.Repeat(" ", maxDiscoveryBody) + meta("example.com/protos", "https://git.example.com/protos")
	pages := map[string]string{"example.com/protos/api": page}
	f := &fakeProber{answer: map[string][]Ref{"https://example.com/protos": nil}}
	o, err := Resolve(context.Background(), Deps{Prober: f, Client: vanityClient(t, pages)}, "example.com/protos/api")
	if err != nil || o.Repo != "https://example.com/protos" || o.Subtree != "api" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
}

// Declarations are honored only in the document head: body content may
// be user-generated, and an injected body declaration must not redirect
// a module. Resolution falls through to probing instead.
func TestResolveVanityIgnoresBodyDeclarations(t *testing.T) {
	page := `<html><head><title>x</title></head><body>` +
		metaTag("example.com/protos", "https://attacker.example.com/protos") + `</body></html>`
	pages := map[string]string{"example.com/protos/api": page}
	f := &fakeProber{answer: map[string][]Ref{"https://example.com/protos": nil}}
	o, err := Resolve(context.Background(), Deps{Prober: f, Client: vanityClient(t, pages)}, "example.com/protos/api")
	if err != nil || o.Repo != "https://example.com/protos" || o.Subtree != "api" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
}

// A matching declaration must name a well-formed HTTPS repository URL: a
// bare scheme or embedded credentials fail resolution, like non-HTTPS.
func TestResolveVanityRejectsMalformedRepoURL(t *testing.T) {
	for name, repo := range map[string]string{
		"bare scheme":          "https://",
		"userinfo":             "https://user:pass@git.example.com/protos",
		"query":                "https://git.example.com/protos?x=1",
		"fragment":             "https://git.example.com/protos#frag",
		"bare query marker":    "https://git.example.com/protos?",
		"bare fragment marker": "https://git.example.com/protos#",
	} {
		t.Run(name, func(t *testing.T) {
			pages := map[string]string{"example.com/protos/api": meta("example.com/protos", repo)}
			_, err := Resolve(context.Background(), Deps{Prober: &fakeProber{}, Client: vanityClient(t, pages)}, "example.com/protos/api")
			if err == nil || !strings.Contains(err.Error(), "malformed repository URL") {
				t.Fatalf("err = %v, want malformed-URL rejection", err)
			}
		})
	}
}

// discoverVanity's own contract, pinned at the seam: failures are
// (zero, false, err) — never a redirect alongside an error — and a
// request that cannot even be constructed is an error, not absence.
func TestDiscoverVanityDirectContracts(t *testing.T) {
	t.Run("unbuildable request URL", func(t *testing.T) {
		red, ok, err := discoverVanity(context.Background(), vanityClient(t, nil), "bad path")
		if err == nil || ok {
			t.Fatalf("red=%+v ok=%v err=%v, want error and no redirect", red, ok, err)
		}
	})
	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		red, ok, err := discoverVanity(ctx, vanityClient(t, nil), "example.com/a")
		if !errors.Is(err, context.Canceled) || ok {
			t.Fatalf("red=%+v ok=%v err=%v, want context.Canceled and no redirect", red, ok, err)
		}
	})
	for name, repo := range map[string]string{
		"non-HTTPS repo":    "ssh://git@example.com/x",
		"unparseable repo":  "https://git.example.com/%zz",
		"credentialed repo": "https://u:p@git.example.com/x",
	} {
		t.Run(name, func(t *testing.T) {
			pages := map[string]string{"example.com/a": meta("example.com/a", repo)}
			red, ok, err := discoverVanity(context.Background(), vanityClient(t, pages), "example.com/a")
			if err == nil || ok {
				t.Fatalf("red=%+v ok=%v err=%v, want error and no redirect", red, ok, err)
			}
		})
	}
}

// errAfterReader yields its content, then fails.
type errAfterReader struct {
	data string
	pos  int
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, errors.New("read failed")
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// A read error during discovery fails closed: declarations already
// parsed from the truncated stream are discarded, not honored.
func TestParsePBImportReaderErrorFailsClosed(t *testing.T) {
	r := &errAfterReader{data: `<html><head>` + metaTag("example.com/a", "https://git.example.com/a")}
	if decls := parsePBImport(r); len(decls) != 0 {
		t.Fatalf("errored read yielded declarations: %+v", decls)
	}
}

// Version-shaped path segments flow through resolution verbatim: the
// module path never gains or loses version segments
// (REQ-resolve-no-import-versioning — paths carry the schema's version
// idiom, never module identity).
func TestVersionSegmentsFlowVerbatim(t *testing.T) {
	f := &fakeProber{answer: map[string][]Ref{"https://example.com/r": nil}}
	o, err := Resolve(context.Background(), Deps{Prober: f, Client: vanityClient(t, nil)}, "example.com/r/foo/v2")
	if err != nil || o.Subtree != "foo/v2" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	if o2, ok := SplitVCS("example.com/r.git/foo/v2"); !ok || o2.Subtree != "foo/v2" {
		t.Fatalf("SplitVCS dropped a version segment: %+v", o2)
	}
}

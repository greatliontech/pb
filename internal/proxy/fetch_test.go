package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// scriptedAttempt answers per source URL/kind and records consultation
// order.
type scriptedAttempt struct {
	answers  map[string]func() (Unverified, error)
	consults []string
}

func (a *scriptedAttempt) try(s Source) (Unverified, error) {
	name := sourceName(s)
	a.consults = append(a.consults, name)
	if f, ok := a.answers[name]; ok {
		return f()
	}
	return nil, fmt.Errorf("%w: scripted absence", ErrNotHere)
}

func notHere() (Unverified, error) { return nil, fmt.Errorf("%w: 404", ErrNotHere) }
func found(b string) func() (Unverified, error) {
	return func() (Unverified, error) { return Unverified(b), nil }
}
func aborts(msg string) func() (Unverified, error) {
	return func() (Unverified, error) { return nil, errors.New(msg) }
}

func mustSources(t *testing.T, pbproxy string) []Source {
	t.Helper()
	cfg, err := ParseConfig(pbproxy, "")
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Sources
}

func TestFallthroughOrderAndNotHere(t *testing.T) {
	a := &scriptedAttempt{answers: map[string]func() (Unverified, error){
		"https://a.example.com": notHere,
		"https://b.example.com": found("artifact"),
	}}
	got, err := Fallthrough(mustSources(t, "https://a.example.com,https://b.example.com,direct"), a.try)
	if err != nil || string(got) != "artifact" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	// b answered; direct is never consulted.
	if fmt.Sprint(a.consults) != "[https://a.example.com https://b.example.com]" {
		t.Fatalf("consultations = %v", a.consults)
	}
}

// Any failure that is not "not here" aborts without consulting later
// sources — a flaky source cannot divert fetches down the list.
func TestFallthroughAbortsOnOtherFailure(t *testing.T) {
	for name, failure := range map[string]func() (Unverified, error){
		"server error":    aborts("unexpected status 500"),
		"transport error": aborts("connection refused"),
		"malformed body":  func() (Unverified, error) { return nil, fmt.Errorf("%w: bad list", ErrMalformed) },
	} {
		t.Run(name, func(t *testing.T) {
			a := &scriptedAttempt{answers: map[string]func() (Unverified, error){
				"https://a.example.com": failure,
				"https://b.example.com": found("never reached"),
			}}
			_, err := Fallthrough(mustSources(t, "https://a.example.com,https://b.example.com"), a.try)
			if err == nil || errors.Is(err, ErrNotHere) {
				t.Fatalf("err = %v, want aborting failure", err)
			}
			if fmt.Sprint(a.consults) != "[https://a.example.com]" {
				t.Fatalf("later source consulted after abort: %v", a.consults)
			}
		})
	}
}

// off fails immediately: nothing after it is consulted, and the attempt
// seam is never invoked for it.
func TestFallthroughOff(t *testing.T) {
	a := &scriptedAttempt{}
	_, err := Fallthrough(mustSources(t, "off,https://a.example.com"), a.try)
	if !errors.Is(err, ErrOff) || !strings.Contains(err.Error(), "entry 1") {
		t.Fatalf("err = %v, want ErrOff naming entry 1", err)
	}
	if len(a.consults) != 0 {
		t.Fatalf("off consulted a source: %v", a.consults)
	}
	// A proxy answering not-here before the off still reaches it.
	a = &scriptedAttempt{answers: map[string]func() (Unverified, error){
		"https://a.example.com": notHere,
	}}
	_, err = Fallthrough(mustSources(t, "https://a.example.com,off"), a.try)
	if !errors.Is(err, ErrOff) {
		t.Fatalf("err = %v, want ErrOff after fall-through", err)
	}
}

// Exhaustion is a not-here failure naming every source's answer.
func TestFallthroughExhausted(t *testing.T) {
	a := &scriptedAttempt{}
	_, err := Fallthrough(mustSources(t, "https://a.example.com,direct"), a.try)
	if !errors.Is(err, ErrNotHere) {
		t.Fatalf("err = %v, want ErrNotHere", err)
	}
	for _, part := range []string{
		"no source has the artifact",
		// Attempts joined with "; ", each naming its source.
		"https://a.example.com: ",
		"; direct: ",
	} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q does not name %q", err, part)
		}
	}
}

// Fallthrough refuses an empty source list loudly — unreachable via
// ParseConfig, which never emits one, but never a silent nil result.
func TestFallthroughEmptySources(t *testing.T) {
	if _, err := Fallthrough(nil, (&scriptedAttempt{}).try); err == nil ||
		!strings.Contains(err.Error(), "empty source list") {
		t.Fatalf("empty sources = %v, want loud refusal", err)
	}
}

// An Attempt answering success with nil bytes violates its contract:
// Fallthrough fails loudly rather than handing callers nothing.
func TestFallthroughNilBytesSuccess(t *testing.T) {
	a := &scriptedAttempt{answers: map[string]func() (Unverified, error){
		"https://a.example.com": func() (Unverified, error) { return nil, nil },
	}}
	_, err := Fallthrough(mustSources(t, "https://a.example.com"), a.try)
	if err == nil || !strings.Contains(err.Error(), "no bytes") {
		t.Fatalf("nil-bytes success = %v, want contract failure", err)
	}
}

// Consultations are always a prefix of the non-off sources, in order —
// independent of which source answers what — stopping at the first
// decisive answer (found, abort, or off), whose outcome matches.
func TestFallthroughConsultationPrefixProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 4).Draw(t, "n")
		var sources []Source
		var names []string
		answers := map[string]func() (Unverified, error){}
		kinds := make([]int, n)
		for i := range n {
			kind := rapid.IntRange(0, 4).Draw(t, fmt.Sprint("kind", i))
			kinds[i] = kind
			name := fmt.Sprintf("https://s%d.example.com", i)
			switch kind {
			case 3:
				sources = append(sources, Source{Off: true})
				name = "off"
			case 4:
				sources = append(sources, Source{Direct: true})
				name = "direct"
			default:
				sources = append(sources, Source{URL: name})
			}
			names = append(names, name)
			switch kind {
			case 1:
				answers[name] = found("artifact")
			case 2:
				answers[name] = aborts("boom")
			}
		}
		a := &scriptedAttempt{answers: answers}
		b, err := Fallthrough(sources, a.try)

		// Expected outcome: walk to the first decisive kind.
		wantConsults := 0
		wantOutcome := "exhausted"
		for _, kind := range kinds {
			if kind == 3 {
				wantOutcome = "off"
				break
			}
			wantConsults++
			if kind == 1 {
				wantOutcome = "found"
				break
			}
			if kind == 2 {
				wantOutcome = "abort"
				break
			}
		}
		if len(a.consults) != wantConsults {
			t.Fatalf("consultations = %v, want %d", a.consults, wantConsults)
		}
		for i, c := range a.consults {
			if c != names[i] {
				t.Fatalf("consultation %d = %q, want %q (order broken)", i, c, names[i])
			}
		}
		switch wantOutcome {
		case "found":
			if err != nil || string(b) != "artifact" {
				t.Fatalf("outcome = (%q, %v), want the artifact", b, err)
			}
		case "off":
			if !errors.Is(err, ErrOff) {
				t.Fatalf("outcome = %v, want ErrOff", err)
			}
		case "abort":
			if err == nil || errors.Is(err, ErrNotHere) || errors.Is(err, ErrOff) {
				t.Fatalf("outcome = %v, want aborting failure", err)
			}
		case "exhausted":
			if !errors.Is(err, ErrNotHere) {
				t.Fatalf("outcome = %v, want ErrNotHere exhaustion", err)
			}
		}
	})
}

// statusRT serves scripted statuses, bodies, and headers fully
// in-process; served bodies track closing, and bodyErrs makes a body
// fail mid-read.
type statusRT struct {
	bodies    map[string]string
	statuses  map[string]int
	headers   map[string]http.Header
	errs      map[string]error
	bodyErrs  map[string]error
	served    []*fetchBody
	requested []string
}

type fetchBody struct {
	io.Reader
	closed bool
}

func (b *fetchBody) Close() error { b.closed = true; return nil }

func (rt *statusRT) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.URL.Host + req.URL.Path
	rt.requested = append(rt.requested, key)
	if err, ok := rt.errs[key]; ok {
		return nil, err
	}
	status := http.StatusOK
	if s, ok := rt.statuses[key]; ok {
		status = s
	}
	body, ok := rt.bodies[key]
	if !ok && status == http.StatusOK {
		status = http.StatusNotFound
		body = "not found"
	}
	var r io.Reader = strings.NewReader(body)
	if err, ok := rt.bodyErrs[key]; ok {
		r = io.MultiReader(r, &errReader{err})
	}
	fb := &fetchBody{Reader: r}
	rt.served = append(rt.served, fb)
	header := http.Header{}
	if h, ok := rt.headers[key]; ok {
		header = h
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       fb,
		Request:    req,
	}, nil
}

type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error) { return 0, r.err }

func TestGetClassification(t *testing.T) {
	rt := &statusRT{
		bodies:   map[string]string{"p.example.com/ok": "body"},
		statuses: map[string]int{},
	}
	client := &http.Client{Transport: rt}
	get := func(path string) (Unverified, error) {
		return Get(context.Background(), client, "https://p.example.com/"+path, 1<<20)
	}
	// 200 yields the body.
	b, err := get("ok")
	if err != nil || string(b) != "body" {
		t.Fatalf("ok = (%q, %v)", b, err)
	}
	// 299 sits inside the 2xx range: accepted.
	rt.bodies["p.example.com/s299"] = "edge"
	rt.statuses["p.example.com/s299"] = 299
	if b, err := get("s299"); err != nil || string(b) != "edge" {
		t.Fatalf("status 299 = (%q, %v), want acceptance", b, err)
	}
	// 404 and 410 are not-here; every other non-2xx aborts — both range
	// edges (199, 300) and statuses below 200 included.
	for status, wantNotHere := range map[int]bool{
		404: true, 410: true, 403: false, 500: false, 302: false,
		150: false, 199: false, 300: false,
	} {
		key := fmt.Sprintf("s%d", status)
		rt.bodies["p.example.com/"+key] = "x"
		rt.statuses["p.example.com/"+key] = status
		_, err := get(key)
		if err == nil {
			t.Fatalf("status %d accepted", status)
		}
		if errors.Is(err, ErrNotHere) != wantNotHere {
			t.Errorf("status %d: err=%v, want notHere=%v", status, err, wantNotHere)
		}
	}
	// Transport failure aborts, never not-here.
	rt.errs = map[string]error{"p.example.com/dead": errors.New("connection refused")}
	if _, err := get("dead"); err == nil || errors.Is(err, ErrNotHere) {
		t.Fatalf("transport error = %v, want aborting failure", err)
	}
	// A body failing mid-read aborts as a read failure.
	rt.bodies["p.example.com/torn"] = "partial"
	rt.bodyErrs = map[string]error{"p.example.com/torn": errors.New("connection reset")}
	if _, err := get("torn"); err == nil || errors.Is(err, ErrNotHere) || !strings.Contains(err.Error(), "reading") {
		t.Fatalf("torn body = %v, want reading failure", err)
	}
	// An unbuildable URL is a build failure before any transport use.
	if _, err := Get(context.Background(), client, "https://bad url", 1<<20); err == nil || !strings.Contains(err.Error(), "building request") {
		t.Fatalf("bad URL = %v, want building-request failure", err)
	}
	// Every served body was closed, success and failure paths alike.
	for i, b := range rt.served {
		if !b.closed {
			t.Errorf("served body %d never closed", i)
		}
	}
}

// The response read is bounded by the caller's artifact-size limit: a
// body at the limit is accepted, one past it aborts before buffering
// more, and a non-positive limit is a caller error.
func TestGetResponseLimit(t *testing.T) {
	rt := &statusRT{bodies: map[string]string{
		"p.example.com/exact": "12345678",
		"p.example.com/over":  "123456789",
	}}
	client := &http.Client{Transport: rt}
	if b, err := Get(context.Background(), client, "https://p.example.com/exact", 8); err != nil || len(b) != 8 {
		t.Fatalf("at-limit = (%d bytes, %v), want acceptance", len(b), err)
	}
	if _, err := Get(context.Background(), client, "https://p.example.com/over", 8); err == nil ||
		errors.Is(err, ErrNotHere) || !strings.Contains(err.Error(), "exceeds the 8-byte limit") {
		t.Fatalf("over-limit = %v, want aborting limit failure", err)
	}
	if _, err := Get(context.Background(), client, "https://p.example.com/exact", 0); err == nil ||
		!strings.Contains(err.Error(), "invalid response limit") {
		t.Fatalf("zero limit = %v, want caller error", err)
	}
	// MaxInt64 would overflow the detection slack into a negative
	// LimitReader bound, silently truncating every body to empty.
	if _, err := Get(context.Background(), client, "https://p.example.com/exact", math.MaxInt64); err == nil ||
		!strings.Contains(err.Error(), "invalid response limit") {
		t.Fatalf("MaxInt64 limit = %v, want caller error", err)
	}
	// One is the smallest valid limit.
	rt.bodies["p.example.com/one"] = "x"
	if b, err := Get(context.Background(), client, "https://p.example.com/one", 1); err != nil || string(b) != "x" {
		t.Fatalf("limit 1 = (%q, %v), want acceptance", b, err)
	}
}

// Proxies legitimately redirect artifacts to blob storage: HTTPS
// redirects are followed with classification on the final response;
// cleartext redirects abort — never not-here.
func TestGetRedirects(t *testing.T) {
	rt := &statusRT{
		bodies:   map[string]string{"blob.example.com/artifact": "artifact"},
		statuses: map[string]int{"p.example.com/redir": 302, "p.example.com/redir404": 302, "p.example.com/cleartext": 302},
		headers: map[string]http.Header{
			"p.example.com/redir":     {"Location": {"https://blob.example.com/artifact"}},
			"p.example.com/redir404":  {"Location": {"https://blob.example.com/gone"}},
			"p.example.com/cleartext": {"Location": {"http://evil.example.com/x"}},
		},
	}
	client := &http.Client{Transport: rt}
	get := func(path string) (Unverified, error) {
		return Get(context.Background(), client, "https://p.example.com/"+path, 1<<20)
	}
	if b, err := get("redir"); err != nil || string(b) != "artifact" {
		t.Fatalf("https redirect = (%q, %v), want the final artifact", b, err)
	}
	// The final hop's 404 classifies: not-here.
	if _, err := get("redir404"); !errors.Is(err, ErrNotHere) {
		t.Fatalf("redirect to 404 = %v, want ErrNotHere from the final response", err)
	}
	// A cleartext redirect aborts — it must not read as not-here and
	// must never be requested.
	_, err := get("cleartext")
	if err == nil || errors.Is(err, ErrNotHere) || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("cleartext redirect = %v, want aborting non-HTTPS failure", err)
	}
	for _, u := range rt.requested {
		if strings.HasPrefix(u, "evil.") {
			t.Fatalf("cleartext target requested: %v", rt.requested)
		}
	}
}

// Redirect chains are bounded at 10 hops: exactly ten requests are
// issued, and a longer chain aborts — never not-here.
func TestGetRedirectCap(t *testing.T) {
	rt := &statusRT{statuses: map[string]int{}, headers: map[string]http.Header{}}
	for i := range 15 {
		key := fmt.Sprintf("h%d.example.com/x", i)
		rt.statuses[key] = 302
		rt.headers[key] = http.Header{"Location": {fmt.Sprintf("https://h%d.example.com/x", i+1)}}
	}
	client := &http.Client{Transport: rt}
	_, err := Get(context.Background(), client, "https://h0.example.com/x", 1<<20)
	if err == nil || errors.Is(err, ErrNotHere) || !strings.Contains(err.Error(), "10 redirects") {
		t.Fatalf("long chain = %v, want the 10-redirect abort", err)
	}
	if len(rt.requested) != 10 {
		t.Fatalf("issued %d requests, want exactly 10 (initial + 9 follows)", len(rt.requested))
	}
}

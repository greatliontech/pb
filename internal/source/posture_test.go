package source

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
)

// chainRT serves a scripted redirect chain fully in-process, recording
// every round trip.
type chainRT struct {
	redirects map[string]string
	bodies    map[string]string
	requested []string
}

func (rt *chainRT) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.URL.Host + req.URL.Path
	rt.requested = append(rt.requested, req.URL.String())
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("cleartext request %q reached the transport", req.URL)
	}
	if loc, ok := rt.redirects[key]; ok {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": {loc}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	}
	body, ok := rt.bodies[key]
	status := http.StatusOK
	if !ok {
		status = http.StatusNotFound
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func TestHTTPClientFollowsHTTPSRedirects(t *testing.T) {
	rt := &chainRT{
		redirects: map[string]string{"a.example.com/x": "https://b.example.com/y"},
		bodies:    map[string]string{"b.example.com/y": "artifact"},
	}
	c := HTTPClient(&http.Client{Transport: rt})
	resp, err := c.Get("https://a.example.com/x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "artifact" {
		t.Fatalf("body = %q, want the redirect target's", b)
	}
}

func TestHTTPClientRefusesCleartextRedirects(t *testing.T) {
	rt := &chainRT{redirects: map[string]string{"a.example.com/x": "http://evil.example.com/y"}}
	c := HTTPClient(&http.Client{Transport: rt})
	_, err := c.Get("https://a.example.com/x") //nolint:bodyclose // error path
	if err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("cleartext redirect = %v, want non-HTTPS refusal", err)
	}
	for _, u := range rt.requested {
		if !strings.HasPrefix(u, "https://") {
			t.Fatalf("cleartext URL requested: %v", rt.requested)
		}
	}
}

// The chain is capped at ten hops: exactly ten requests are issued.
func TestHTTPClientRedirectCap(t *testing.T) {
	rt := &chainRT{redirects: map[string]string{}}
	for i := range 15 {
		rt.redirects[fmt.Sprintf("h%d.example.com/x", i)] = fmt.Sprintf("https://h%d.example.com/x", i+1)
	}
	c := HTTPClient(&http.Client{Transport: rt})
	_, err := c.Get("https://h0.example.com/x") //nolint:bodyclose // error path
	if err == nil || !strings.Contains(err.Error(), "10 redirects") {
		t.Fatalf("long chain = %v, want the 10-redirect refusal", err)
	}
	if len(rt.requested) != 10 {
		t.Fatalf("issued %d requests, want exactly 10 (initial + 9 follows)", len(rt.requested))
	}
}

// The copy is cookie-free and the base client is never mutated.
func TestHTTPClientStripsJarAndPreservesBase(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	u := &url.URL{Scheme: "https", Host: "a.example.com", Path: "/x"}
	jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: "secret"}})
	base := &http.Client{Jar: jar}
	c := HTTPClient(base)
	if c.Jar != nil {
		t.Fatal("policy client carries a cookie jar")
	}
	if c == base {
		t.Fatal("policy client is the base, not a copy")
	}
	if base.Jar == nil || base.CheckRedirect != nil {
		t.Fatal("base client mutated: jar stripped or CheckRedirect installed")
	}
}

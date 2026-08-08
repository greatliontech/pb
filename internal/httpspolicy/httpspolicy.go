// Package httpspolicy pins the one fetch posture pb's HTTP requests
// share — vanity discovery (module-resolution.md, REQ-resolve-vanity's
// redirect clause) and proxy artifact fetches (module-proxy.md,
// REQ-proxy-fallthrough's): redirects only to HTTPS URLs and boundedly,
// no cookie state crossing a fetch in either direction. Both clauses
// pin the same posture; one code home keeps them from drifting apart.
package httpspolicy

import (
	"fmt"
	"net/http"
)

// Client returns a shallow copy of base pinning the shared policy: the
// copy keeps base's transport and timeout, carries no cookie jar —
// fetched hosts never see caller cookies, responses never seed a jar —
// and follows redirects only to HTTPS URLs, at most nine follows (ten
// requests; a custom CheckRedirect replaces the default cap, so the
// stdlib count is restated). base itself is never mutated.
func Client(base *http.Client) *http.Client {
	c := *base
	c.Jar = nil
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("redirect to non-HTTPS URL %q", req.URL)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	return &c
}

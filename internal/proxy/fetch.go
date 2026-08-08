package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/greatliontech/pb/internal/httpspolicy"
)

// ErrNotHere is wrapped when a source answers "not here" — for a proxy,
// exactly a 404 or 410 (REQ-proxy-not-found); these are the only
// failures that move the fetch to the next source
// (REQ-proxy-fallthrough).
var ErrNotHere = errors.New("artifact not here")

// ErrOff is wrapped when the consulted source list refuses the fetch:
// an off entry fails when reached, consulting nothing further.
var ErrOff = errors.New("fetching disabled by source list (off)")

// Unverified carries fetched artifact bytes no digest or pin has
// vouched for (REQ-proxy-client-verification): transport success is
// never acceptance, and every consumer verifies per module-archive.md
// and module-lockfile.md before use. The named type keeps that state
// visible at every seam the bytes cross.
type Unverified []byte

// Attempt consults one source for one artifact: non-nil Unverified
// bytes on success, an ErrNotHere-wrapped error when the source does
// not have it, any other error to abort the fetch. The direct source's
// attempt fetches from the origin.
type Attempt func(Source) (Unverified, error)

// Fallthrough tries sources in order (REQ-proxy-fallthrough): the next
// source is consulted only when the previous answered not-here; any
// other failure — transport error, unexpected status, malformed
// response — aborts without consulting later sources, so a flaky or
// hostile source cannot silently divert fetches down the list. An off
// source fails when reached. Exhausting the list is a not-here failure
// carrying every source's answer.
func Fallthrough(sources []Source, try Attempt) (Unverified, error) {
	if len(sources) == 0 {
		return nil, errors.New("empty source list")
	}
	var attempts []string
	for i, s := range sources {
		if s.Off {
			return nil, fmt.Errorf("%w: entry %d of the source list", ErrOff, i+1)
		}
		b, err := try(s)
		if err == nil {
			// A nil-bytes success is an Attempt contract violation, not
			// an artifact: fail loudly rather than hand callers nothing.
			if b == nil {
				return nil, fmt.Errorf("source %s answered success with no bytes", sourceName(s))
			}
			return b, nil
		}
		if !errors.Is(err, ErrNotHere) {
			return nil, err
		}
		attempts = append(attempts, fmt.Sprintf("%s: %v", sourceName(s), err))
	}
	return nil, fmt.Errorf("%w: no source has the artifact (%s)",
		ErrNotHere, strings.Join(attempts, "; "))
}

// sourceName labels a consulted source for diagnostics. Off sources are
// never named: Fallthrough fails on them before any consultation.
func sourceName(s Source) string {
	if s.Direct {
		return "direct"
	}
	return s.URL
}

// Get fetches one proxy endpoint and classifies the answer
// (REQ-proxy-not-found): 404 and 410 are the only "not here" statuses —
// applied to the final response after HTTPS-only bounded redirects
// (REQ-proxy-fallthrough); any other non-2xx, any transport failure, a
// cleartext or excessive redirect, and a response larger than limit
// bytes all abort the fetch. limit is the caller's artifact-size bound
// (a response beyond REQ-archive-size-limit can never verify, so
// reading it would trust the proxy with unbounded memory). The returned
// bytes are Unverified — nothing about a 200 is trusted.
func Get(ctx context.Context, client *http.Client, url string, limit int64) (Unverified, error) {
	// MaxInt64 is excluded so the detection slack below cannot overflow
	// into a negative LimitReader bound, which would silently truncate
	// every body to empty.
	if limit <= 0 || limit == math.MaxInt64 {
		return nil, fmt.Errorf("fetching %s: invalid response limit %d (must be positive and below MaxInt64)", url, limit)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", url, err)
	}
	// Proxies legitimately redirect artifact fetches to blob storage,
	// under the shared fetch posture: cookie-free, redirects HTTPS-only
	// and bounded.
	resp, err := httpspolicy.Client(client).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, fmt.Errorf("%w: %s answered %d", ErrNotHere, url, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, fmt.Errorf("fetching %s: unexpected status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("reading %s: response exceeds the %d-byte limit", url, limit)
	}
	return Unverified(body), nil
}

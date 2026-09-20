package origin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/greatliontech/pb/internal/source"
	"golang.org/x/net/html"
)

// maxDiscoveryBody bounds how much of a discovery response is read: the
// body is attacker-controlled and pb-import metas sit in <head>, so a
// declaration past this limit is treated as absent rather than the read
// being unbounded.
const maxDiscoveryBody = 1 << 20

// redirect is one vanity declaration: a module-path prefix and the HTTPS
// repository backing it.
type redirect struct {
	Prefix string
	Repo   string
}

// discoverVanity performs the one discovery request of REQ-resolve-vanity:
// GET https://<module path>?pb-get=1. The response may declare redirects
// for any of the path's prefixes via
// `<meta name="pb-import" content="<prefix> git <repository URL>">`; the
// longest segment-exact matching prefix wins. A transport failure or a
// non-2xx response is absence — resolution falls through to probing — but
// a matching declaration whose repository URL is not HTTPS fails
// resolution rather than redirecting. Redirects are followed only across
// HTTPS: a hop to any other scheme aborts the request, which is absence
// like any other transport failure — nothing is ever fetched over
// cleartext, and probing is HTTPS-only regardless.
func discoverVanity(ctx context.Context, client *http.Client, path string) (redirect, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+path+"?pb-get=1", nil)
	if err != nil {
		return redirect{}, false, fmt.Errorf("vanity request for %s: %w", path, err)
	}
	// Discovery pins the shared fetch posture — cookie-free, redirects
	// HTTPS-only and bounded — on a copy; the caller's client is
	// untouched.
	resp, err := source.HTTPClient(client).Do(req)
	if err != nil {
		// Absence and cancellation both surface as transport errors;
		// only a live context means the prefix genuinely didn't answer.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return redirect{}, false, fmt.Errorf("vanity discovery for %s: %w", path, ctxErr)
		}
		return redirect{}, false, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return redirect{}, false, nil
	}
	decls := parsePBImport(io.LimitReader(resp.Body, maxDiscoveryBody))
	best := redirect{}
	found := false
	for _, d := range decls {
		if _, ok := subtreeOf(path, d.Prefix); !ok {
			continue
		}
		if !found || len(d.Prefix) > len(best.Prefix) {
			best, found = d, true
		}
	}
	if !found {
		return redirect{}, false, nil
	}
	u, err := url.Parse(best.Repo)
	if err != nil || u.Scheme != "https" {
		return redirect{}, false, fmt.Errorf("vanity redirect for %s names non-HTTPS repository %q", path, best.Repo)
	}
	// The declaration is attacker-influenced page content: a hostless URL
	// is degenerate, embedded userinfo would inject credentials into the
	// git client, and a query or fragment has no meaning on a git remote
	// — rejecting here fails crisply instead of surfacing later as a
	// mangled protocol request. Bare markers count too: url.Parse reports
	// a trailing "?" as ForceQuery with an empty RawQuery, and a bare "#"
	// (necessarily URL-final, since a fragment is everything after it)
	// leaves Fragment empty.
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		u.ForceQuery || strings.HasSuffix(best.Repo, "#") {
		return redirect{}, false, fmt.Errorf("vanity redirect for %s names malformed repository URL %q: HTTPS URL must carry a host and no credentials, query, or fragment", path, best.Repo)
	}
	return best, true, nil
}

// parsePBImport walks the discovery HTML for pb-import meta declarations,
// honoring only those in the document head: body content is where hosts
// place user-generated HTML, and an injected body declaration must not
// redirect a module. Byte-level pre-slicing of <head>…</head> would be
// weaker — it misses implicit heads and trips on tag case, attributes,
// and comments — so the restriction is on the parsed tree. Malformed HTML
// is parsed as far as the parser allows — html.Parse never fails on
// content — and malformed declarations are ignored.
func parsePBImport(r io.Reader) []redirect {
	doc, err := html.Parse(r)
	if err != nil {
		return nil
	}
	var findHead func(*html.Node) *html.Node
	findHead = func(n *html.Node) *html.Node {
		if n.Type == html.ElementNode && n.Data == "head" {
			return n
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if h := findHead(c); h != nil {
				return h
			}
		}
		return nil
	}
	head := findHead(doc)
	if head == nil {
		return nil
	}
	var out []redirect
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "meta" {
			var name, content string
			for _, a := range n.Attr {
				switch a.Key {
				case "name":
					name = a.Val
				case "content":
					content = a.Val
				}
			}
			if name == "pb-import" {
				if fields := strings.Fields(content); len(fields) == 3 && fields[1] == "git" {
					out = append(out, redirect{Prefix: fields[0], Repo: fields[2]})
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(head)
	return out
}

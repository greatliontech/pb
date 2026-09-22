package proxy

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/greatliontech/pb/internal/source"
)

// ErrConfig is wrapped by every proxy configuration rejection
// (REQ-proxy-config): a malformed source entry or exclusion pattern
// is a configuration error, never a silent non-match.
var ErrConfig = errors.New("invalid proxy configuration")

// Source is one fetch source (the source-list term): a proxy by base
// URL, the origin directly, or the refusal to fetch.
type Source struct {
	URL    string // proxy base URL; empty for Direct and Off
	Direct bool
	Off    bool
}

// Config is the parsed source-list configuration (REQ-proxy-config):
// the ordered sources from the proxy setting and the noproxy
// setting's patterns routing matching modules to the origin.
type Config struct {
	Sources []Source
	NoProxy []string
}

// ParseSources parses the proxy setting's value (REQ-proxy-config;
// the caller attributes a refusal to the layer the value came from):
// comma-separated entries, each a proxy base URL satisfying the
// endpoint constructors' precondition, direct, or off. An empty
// value is direct alone — there is no default proxy, so no party
// beyond the origin host is trusted for a first fetch.
func ParseSources(list string) ([]Source, error) {
	if list == "" {
		return []Source{{Direct: true}}, nil
	}
	var sources []Source
	for entry := range strings.SplitSeq(list, ",") {
		switch entry {
		case "":
			return nil, fmt.Errorf("%w: the source list carries an empty entry", ErrConfig)
		case "direct":
			sources = append(sources, Source{Direct: true})
		case "off":
			sources = append(sources, Source{Off: true})
		default:
			if err := checkBaseURL(entry); err != nil {
				return nil, fmt.Errorf("%w: source %q: %v", ErrConfig, entry, err)
			}
			sources = append(sources, Source{URL: entry})
		}
	}
	return sources, nil
}

// ParseNoProxy parses the noproxy setting's value: comma-separated
// glob patterns in the shared grammar (source.ParsePatterns), each
// valid; an empty value is no pattern.
func ParseNoProxy(list string) ([]string, error) {
	patterns, err := source.ParsePatterns(list, "exclusion")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConfig, err)
	}
	return patterns, nil
}

// checkBaseURL enforces the endpoint constructors' precondition on a
// proxy entry: an absolute http(s) URL with a host and no query,
// fragment, or credentials.
func checkBaseURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return err
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("scheme %q is not http or https", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("URL carries no host")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.HasSuffix(s, "#") || u.User != nil {
		return errors.New("URL must carry no query, fragment, or credentials")
	}
	// join trims exactly one trailing slash; a second would put an empty
	// segment in every endpoint URL, 404 at the proxy, and silently fall
	// the fetch through to later sources.
	if strings.HasSuffix(s, "//") {
		return errors.New("URL carries multiple trailing slashes")
	}
	return nil
}

// SourcesFor returns the sources consulted for a module: a noproxy
// match routes to the origin regardless of the source list
// (REQ-proxy-config), even a list that is off. The unmatched case
// returns the Config's own slice — callers read, never mutate.
func (c Config) SourcesFor(modulePath string) []Source {
	if source.MatchAny(c.NoProxy, modulePath) {
		return []Source{{Direct: true}}
	}
	return c.Sources
}

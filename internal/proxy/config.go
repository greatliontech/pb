package proxy

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
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
// glob patterns, each valid; an empty value is no pattern.
func ParseNoProxy(list string) ([]string, error) {
	if list == "" {
		return nil, nil
	}
	var patterns []string
	for pat := range strings.SplitSeq(list, ",") {
		if pat == "" {
			return nil, fmt.Errorf("%w: the exclusion list carries an empty pattern", ErrConfig)
		}
		if err := checkPattern(pat); err != nil {
			return nil, fmt.Errorf("%w: exclusion pattern %q: %v", ErrConfig, pat, err)
		}
		patterns = append(patterns, pat)
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

// checkPattern validates a glob eagerly with the matching engine
// itself: path.Match validates the entire pattern on every call (Go
// 1.16+), so a single probe is a complete validity oracle and the
// pattern grammar has exactly one owner. matchPrefixPattern may then
// discard Match errors — none can occur for a validated pattern.
func checkPattern(pat string) error {
	_, err := path.Match(pat, "")
	return err
}

// SourcesFor returns the sources consulted for a module: a noproxy
// match routes to the origin regardless of the source list
// (REQ-proxy-config), even a list that is off. The unmatched case
// returns the Config's own slice — callers read, never mutate.
func (c Config) SourcesFor(modulePath string) []Source {
	for _, pat := range c.NoProxy {
		if matchPrefixPattern(pat, modulePath) {
			return []Source{{Direct: true}}
		}
	}
	return c.Sources
}

// matchPrefixPattern reports whether the glob matches the module path or
// any leading segment prefix of it (REQ-proxy-config's matching rule);
// path.Match's '*' never crosses a '/', so matching stays segment-wise.
// Patterns are validated at parse, so a Match error cannot occur here
// and reads as a non-match.
func matchPrefixPattern(pattern, modulePath string) bool {
	prefix := modulePath
	for {
		if ok, _ := path.Match(pattern, prefix); ok {
			return true
		}
		i := strings.LastIndexByte(prefix, '/')
		if i < 0 {
			return false
		}
		prefix = prefix[:i]
	}
}

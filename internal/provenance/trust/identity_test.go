package trust

import (
	"errors"
	"strings"
	"testing"

	"github.com/greatliontech/glob"
)

func TestExplicitIdentity(t *testing.T) {
	t.Run("happy: san glob passes through verbatim, issuer exact", func(t *testing.T) {
		id, err := ExplicitIdentity(IdentityRule{SAN: "https://github.com/acme/**", Issuer: "https://x"})
		if err != nil {
			t.Fatalf("ExplicitIdentity = %v, want nil", err)
		}
		if id.Issuer != "https://x" || id.SubjectGlob != "https://github.com/acme/**" {
			t.Fatalf("identity = %+v", id)
		}
		if id.Subject != "" || id.SubjectRegex != "" {
			t.Fatalf("identity carries a non-glob subject kind: %+v", id)
		}
	})
	t.Run("invalid glob fails closed", func(t *testing.T) {
		if _, err := ExplicitIdentity(IdentityRule{SAN: "[a", Issuer: "https://x"}); err == nil {
			t.Fatal("ExplicitIdentity(invalid glob) = nil, want error")
		}
	})
	t.Run("empty SAN fails closed", func(t *testing.T) {
		// Parse already rejects an empty san, but a directly constructed
		// rule must not slip through either: the empty glob is the
		// unset-kind sentinel, which the verifier's validation rejects.
		if _, err := ExplicitIdentity(IdentityRule{SAN: "", Issuer: "https://x"}); err == nil {
			t.Fatal("ExplicitIdentity(empty san) = nil, want error")
		}
	})
}

// defaultMatches reports whether the DefaultIdentity subject pattern
// for repoURL accepts san, through the same glob semantics the
// verifier applies.
func defaultMatches(t *testing.T, repoURL, san string) bool {
	t.Helper()
	id, err := DefaultIdentity(repoURL)
	if err != nil {
		t.Fatalf("DefaultIdentity(%q) = %v", repoURL, err)
	}
	return glob.MustCompile(id.SubjectGlob).Match(san)
}

func TestDefaultIdentity(t *testing.T) {
	t.Run("github origin yields the workflow-URI policy", func(t *testing.T) {
		id, err := DefaultIdentity("https://github.com/acme/widget")
		if err != nil {
			t.Fatalf("DefaultIdentity = %v, want nil", err)
		}
		if id.Issuer != "https://token.actions.githubusercontent.com" {
			t.Fatalf("issuer = %q", id.Issuer)
		}
		if id.SubjectGlob == "" || id.SubjectRegex != "" || id.Subject != "" {
			t.Fatalf("identity = %+v, want a glob-kind subject", id)
		}
	})
	t.Run("accepts exactly the SANs under the repository URL", func(t *testing.T) {
		const url = "https://github.com/acme/widget"
		accepted := []string{
			url + "/.github/workflows/release.yml@refs/heads/main",
			url + "/.github/workflows/ci.yml@refs/tags/v1.2.3",
			url + "/x",
			// A raw LF is not a valid URI octet and Fulcio derives
			// workflow SANs from claim values that cannot carry one, so
			// this shape is unreachable through any chain-valid
			// certificate; the clause pins "under the URL" and nothing
			// else, and this pin keeps the behavior from silently
			// flapping. (The retired regex default rejected it only as an
			// accident of RE2 `.` excluding newlines.)
			url + "/a\nb",
		}
		rejected := []string{
			url, // the bare URL is not a designation
			"https://github.com/acme/widgetx/.github/workflows/x.yml@refs/heads/main",
			"https://github.com/acme/wid/.github/workflows/x.yml@refs/heads/main",
			"https://github.com/other/widget/x",
		}
		for _, san := range accepted {
			if !defaultMatches(t, url, san) {
				t.Errorf("SAN %q rejected, want accepted", san)
			}
		}
		for _, san := range rejected {
			if defaultMatches(t, url, san) {
				t.Errorf("SAN %q accepted, want rejected", san)
			}
		}
	})
	t.Run("pattern syntax in the repo URL is neutralized", func(t *testing.T) {
		if defaultMatches(t, "https://github.com/acme/wid*t", "https://github.com/acme/widget/x") {
			t.Fatal("a metacharacter-bearing repo URL must match only itself literally")
		}
	})
	t.Run("an oversized repo URL fails closed at pattern limits", func(t *testing.T) {
		// Quoting at most doubles the URL; past the pattern byte limit
		// the glob no longer compiles and the constructed identity must
		// be rejected, never silently accepted with a broken pattern.
		long := "https://github.com/acme/" + strings.Repeat("a", 8000)
		if _, err := DefaultIdentity(long); err == nil ||
			!strings.Contains(err.Error(), "default identity") {
			t.Fatalf("DefaultIdentity(oversized) = %v, want validation failure", err)
		}
	})
	t.Run("trailing slash on the repo URL does not double", func(t *testing.T) {
		a, err := DefaultIdentity("https://github.com/acme/widget")
		if err != nil {
			t.Fatal(err)
		}
		b, err := DefaultIdentity("https://github.com/acme/widget/")
		if err != nil {
			t.Fatal(err)
		}
		if a.SubjectGlob != b.SubjectGlob {
			t.Fatalf("globs differ: %q vs %q", a.SubjectGlob, b.SubjectGlob)
		}
	})
	t.Run("unknown forge has no default", func(t *testing.T) {
		if _, err := DefaultIdentity("https://git.example.org/acme/widget"); !errors.Is(err, ErrNoDefaultIdentity) {
			t.Fatalf("DefaultIdentity = %v, want ErrNoDefaultIdentity", err)
		}
	})
	t.Run("scheme-less repo URL resolves its host", func(t *testing.T) {
		id, err := DefaultIdentity("github.com/acme/widget")
		if err != nil {
			t.Fatalf("DefaultIdentity = %v, want nil: the host is github.com with or without a scheme", err)
		}
		if id.Issuer != "https://token.actions.githubusercontent.com" {
			t.Fatalf("issuer = %q", id.Issuer)
		}
	})
	t.Run("bare-host URL neither panics nor mis-slices", func(t *testing.T) {
		id, err := DefaultIdentity("https://github.com")
		if err != nil {
			t.Fatalf("DefaultIdentity = %v, want nil", err)
		}
		if !strings.HasSuffix(id.SubjectGlob, "/*/**") {
			t.Fatalf("subject glob = %q", id.SubjectGlob)
		}
	})
}

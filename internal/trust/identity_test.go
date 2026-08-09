package trust

import (
	"errors"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/rapidtest"
)

func TestMain(m *testing.M) { rapidtest.Main(m) }

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

func TestDefaultIdentity(t *testing.T) {
	t.Run("github origin yields the workflow-URI policy", func(t *testing.T) {
		id, err := DefaultIdentity("https://github.com/acme/widget")
		if err != nil {
			t.Fatalf("DefaultIdentity = %v, want nil", err)
		}
		if id.Issuer != "https://token.actions.githubusercontent.com" {
			t.Fatalf("issuer = %q", id.Issuer)
		}
		if !strings.Contains(id.SubjectRegex, `github\.com/acme/widget`) ||
			!strings.HasSuffix(id.SubjectRegex, "/.*") {
			t.Fatalf("subject regex = %q", id.SubjectRegex)
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
		if a.SubjectRegex != b.SubjectRegex {
			t.Fatalf("regexes differ: %q vs %q", a.SubjectRegex, b.SubjectRegex)
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
		if !strings.HasSuffix(id.SubjectRegex, "/.*") {
			t.Fatalf("subject regex = %q", id.SubjectRegex)
		}
	})
}

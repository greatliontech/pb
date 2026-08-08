package direct

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/greatliontech/pb/internal/origin"
	"github.com/greatliontech/pb/internal/version"
	"pgregory.net/rapid"
)

func mustParse(t failer, s string) version.Version {
	v, err := version.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func mustPseudo(t failer, precedent *version.Version, when time.Time, hash string) version.Version {
	v, err := version.PseudoVersion(precedent, when, hash)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// graphFixture is the shared origin: a three-commit main history, an
// orphan branch, and tags in disjoint subtree namespaces so each case
// reads its own release history off the same commit graph.
//
//	C0 (orphan)              C1 -- C2 -- C3 (main, HEAD)
//	decoy/v2.0.0             |     |     `self/v3.0.0
//	                         |     |-lw/v1.5.0
//	                         |     `pre/v1.1.0-rc.1
//	                         |-v1.0.0 (annotated)
//	                         `ps/v1.0.1-0.20260101000000-aaaaaaaaaaaa
type graphFixture struct {
	*repoFixture
	c0, c1, c2, c3 plumbing.Hash
	t0, t1, t2, t3 time.Time
	tagV1          plumbing.Hash // the v1.0.0 annotated tag object
	// The diamond on the feature branch: cM merges cA (child of c1) and
	// cB, itself a merge of c1 and the tagged side root cD — so walking
	// cM's ancestry revisits c1 before it can reach cD's tag.
	cD, cA, cB, cM plumbing.Hash
}

func newGraphFixture(t failer) *graphFixture {
	f := &graphFixture{repoFixture: newFixture(t)}
	f.t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.t1 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	// A non-UTC committer offset pins the UTC rendering of embedded
	// pseudo-version timestamps.
	f.t2 = time.Date(2026, 2, 2, 3, 4, 5, 0, time.FixedZone("EET", 2*60*60))
	f.t3 = time.Date(2026, 3, 2, 3, 4, 5, 0, time.UTC)

	f.c0 = f.commit("c0", f.t0)
	f.c1 = f.commit("c1", f.t1)
	f.c2 = f.commit("c2", f.t2, f.c1)
	f.c3 = f.commit("c3", f.t3, f.c2)

	f.branch("orphan", f.c0)
	f.branch("main", f.c3)
	f.head("main")

	f.annotatedTag("v1.0.0", f.c1, plumbing.CommitObject, f.t1)
	ref, err := f.st.Reference(plumbing.ReferenceName("refs/tags/v1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	f.tagV1 = ref.Hash()

	f.tag("lw/v1.5.0", f.c2)
	f.tag("pre/v1.1.0-rc.1", f.c2)
	f.tag("self/v3.0.0", f.c3)
	f.tag("ps/v1.0.1-0.20260101000000-aaaaaaaaaaaa", f.c1)
	f.tag("decoy/v2.0.0", f.c0)
	f.annotatedTag("obj/v1.0.0", f.tree("objtag"), plumbing.TreeObject, f.t1)

	// Higher-ranked non-releases above a real release: base derivation
	// must skip them and keep scanning, not stop.
	f.tag("ps2/v1.0.0", f.c1)
	f.tag("ps2/v2.0.1-0.20260101000000-cccccccccccc", f.c1)
	f.tag("obj2/v1.0.0", f.c1)
	f.annotatedTag("obj2/v5.0.0", f.tree("objtag2"), plumbing.TreeObject, f.t1)

	f.cD = f.commit("d", f.t0)
	f.cA = f.commit("a", f.t2, f.c1)
	f.cB = f.commit("b", f.t2, f.c1, f.cD)
	f.cM = f.commit("m", f.t3, f.cA, f.cB)
	f.branch("feature", f.cM)
	f.tag("merge/v4.0.0", f.cD)
	return f
}

func TestResolveReleaseTags(t *testing.T) {
	f := newGraphFixture(t)
	repo := f.fetch()
	cases := []struct {
		version string
		subtree string
		commit  plumbing.Hash
		when    time.Time
	}{
		{"v1.0.0", "", f.c1, f.t1},   // annotated, root namespace
		{"v1.5.0", "lw", f.c2, f.t2}, // lightweight, subtree namespace
	}
	for _, c := range cases {
		got, err := repo.ResolveVersion(mustParse(t, c.version), c.subtree)
		if err != nil {
			t.Fatalf("ResolveVersion(%s, %q): %v", c.version, c.subtree, err)
		}
		if got.Hash != c.commit.String() || !got.Time.Equal(c.when) {
			t.Fatalf("ResolveVersion(%s, %q) = %+v, want %s at %s", c.version, c.subtree, got, c.commit, c.when)
		}
	}
}

func TestResolveUnknownVersionFails(t *testing.T) {
	f := newGraphFixture(t)
	repo := f.fetch()
	if _, err := repo.ResolveVersion(mustParse(t, "v9.9.9"), ""); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("err = %v, want ErrUnknownVersion", err)
	}
	// A tag present only in another namespace is not this module's.
	if _, err := repo.ResolveVersion(mustParse(t, "v1.5.0"), ""); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("err = %v, want ErrUnknownVersion for foreign-namespace tag", err)
	}
}

func TestResolveTagNamingNonCommitFails(t *testing.T) {
	f := newGraphFixture(t)
	repo := f.fetch()
	_, err := repo.ResolveVersion(mustParse(t, "v1.0.0"), "obj")
	if err == nil || errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("err = %v, want a non-commit tag failure", err)
	}
}

func TestResolvePseudoBases(t *testing.T) {
	f := newGraphFixture(t)
	repo := f.fetch()
	v100 := mustParse(t, "v1.0.0")
	rc := mustParse(t, "v1.1.0-rc.1")
	v150 := mustParse(t, "v1.5.0")
	v300 := mustParse(t, "v3.0.0")
	v400 := mustParse(t, "v4.0.0")
	cases := []struct {
		name      string
		subtree   string
		commit    plumbing.Hash
		when      time.Time
		precedent *version.Version
	}{
		{"release tag on ancestor seeds base", "", f.c3, f.t3, &v100},
		{"prerelease tag seeds continuation base", "pre", f.c3, f.t3, &rc},
		{"tag on the commit itself seeds base", "self", f.c3, f.t3, &v300},
		{"no tag on any ancestor yields zero base", "", f.c0, f.t0, nil},
		{"tag on non-ancestor is ignored", "decoy", f.c3, f.t3, nil},
		{"pseudo-shaped tag is not a release", "ps", f.c3, f.t3, nil},
		{"tag naming a non-commit is skipped", "obj", f.c3, f.t3, nil},
		{"pseudo-shaped tag above a release is skipped, not terminal", "ps2", f.c3, f.t3, &v100},
		{"non-commit tag above a release is skipped, not terminal", "obj2", f.c3, f.t3, &v100},
		{"merge ancestry reaches a side-branch tag", "merge", f.cM, f.t3, &v400},
		// c2's committer offset is +02:00: the embedded timestamp must
		// render in UTC on both sides of the round trip.
		{"non-UTC committer offset renders UTC in the timestamp", "lw", f.c2, f.t2, &v150},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := mustPseudo(t, c.precedent, c.when, c.commit.String())
			got, err := repo.ResolveVersion(v, c.subtree)
			if err != nil {
				t.Fatalf("ResolveVersion(%s, %q): %v", v, c.subtree, err)
			}
			if got.Hash != c.commit.String() {
				t.Fatalf("ResolveVersion(%s, %q) = %s, want %s", v, c.subtree, got.Hash, c.commit)
			}
		})
	}
}

func TestResolvePseudoCraftedBaseFails(t *testing.T) {
	f := newGraphFixture(t)
	repo := f.fetch()
	v99 := mustParse(t, "v99.0.0")
	cases := []struct {
		name    string
		subtree string
		v       version.Version
	}{
		{"inflated base outranking every release", "", mustPseudo(t, &v99, f.t3, f.c3.String())},
		{"zero base hiding a real release", "", mustPseudo(t, nil, f.t3, f.c3.String())},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := repo.ResolveVersion(c.v, c.subtree); !errors.Is(err, ErrBaseInconsistent) {
				t.Fatalf("err = %v, want ErrBaseInconsistent", err)
			}
		})
	}
}

func TestResolvePseudoAbsentCommitFails(t *testing.T) {
	f := newGraphFixture(t)
	repo := f.fetch()
	v := mustPseudo(t, nil, f.t3, "abcdefabcdefabcdefabcdefabcdefabcdefabcd")
	if _, err := repo.ResolveVersion(v, ""); !errors.Is(err, ErrCommitAbsent) {
		t.Fatalf("err = %v, want ErrCommitAbsent", err)
	}
}

func TestResolvePseudoTimeMismatchFails(t *testing.T) {
	f := newGraphFixture(t)
	repo := f.fetch()
	// Right commit, wrong embedded time: binding must fail before any
	// base derivation applies.
	v := mustPseudo(t, nil, f.t3.Add(time.Hour), f.c3.String())
	if _, err := repo.ResolveVersion(v, "decoy"); !errors.Is(err, origin.ErrPseudoMismatch) {
		t.Fatalf("err = %v, want origin.ErrPseudoMismatch", err)
	}
}

// uniqueCommit's ambiguity arm cannot be reached through a fixture — two
// commits sharing a 48-bit hash prefix are not constructible — so the
// decision is pinned directly, which is why it is a pure function.
func TestUniqueCommitDecision(t *testing.T) {
	v := mustPseudo(t, nil, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := &object.Commit{Hash: plumbing.NewHash("aaaaaaaaaaaa1111111111111111111111111111")}
	b := &object.Commit{Hash: plumbing.NewHash("aaaaaaaaaaaa2222222222222222222222222222")}

	if _, err := uniqueCommit(v, nil); !errors.Is(err, ErrCommitAbsent) {
		t.Fatalf("empty: err = %v, want ErrCommitAbsent", err)
	}
	got, err := uniqueCommit(v, []*object.Commit{a})
	if err != nil || got != a {
		t.Fatalf("single: got %v, %v; want the commit", got, err)
	}
	// Matches arrive in storage iteration order; the report must not
	// depend on it, so the reversed input still names the hashes sorted.
	_, err = uniqueCommit(v, []*object.Commit{b, a})
	if !errors.Is(err, ErrCommitAmbiguous) {
		t.Fatalf("double: err = %v, want ErrCommitAmbiguous", err)
	}
	if !strings.Contains(err.Error(), a.Hash.String()+", "+b.Hash.String()) {
		t.Fatalf("ambiguity error %q does not name both commits in sorted order", err)
	}
}

func TestHeadResolvesDefaultBranch(t *testing.T) {
	f := newGraphFixture(t)
	repo := f.fetch()
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	if head.Hash != f.c3.String() || !head.Time.Equal(f.t3) {
		t.Fatalf("Head() = %+v, want %s at %s", head, f.c3, f.t3)
	}
}

func TestRefsAdvertisePeeledTags(t *testing.T) {
	f := newGraphFixture(t)
	repo := f.fetch()
	refs, err := repo.Refs()
	if err != nil {
		t.Fatal(err)
	}
	tags := origin.ReleaseTags(refs, "")
	if len(tags) != 1 {
		t.Fatalf("root release tags = %+v, want exactly v1.0.0", tags)
	}
	got := tags[0]
	if got.Version.String() != "v1.0.0" || got.Hash != f.tagV1.String() || got.Peeled != f.c1.String() {
		t.Fatalf("v1.0.0 = %+v, want tag object %s peeled to %s", got, f.tagV1, f.c1)
	}
}

// The synthesized fallback wires end to end: the head this layer
// resolves is the commit the fallback pseudo-version names, and that
// pseudo-version resolves back to the same commit.
func TestSynthesizedFallbackResolvesHeadPseudo(t *testing.T) {
	f := newFixture(t)
	when := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	c := f.commit("only", when)
	f.branch("main", c)
	f.head("main")
	repo := f.fetch()

	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	refs, err := repo.Refs()
	if err != nil {
		t.Fatal(err)
	}
	tags, err := origin.SynthesizedVersions(refs, head)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || !tags[0].Version.IsPseudo() {
		t.Fatalf("SynthesizedVersions = %+v, want one head pseudo-version", tags)
	}
	got, err := repo.ResolveVersion(tags[0].Version, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Hash != c.String() {
		t.Fatalf("fallback pseudo resolves to %s, want head %s", got.Hash, c)
	}
}

func TestFetchEmptyOriginFails(t *testing.T) {
	f := newFixture(t)
	fe := Fetcher{clientOptions: f.fetchClientOptions()}
	if _, err := fe.Fetch(t.Context(), "file:///"); err == nil {
		t.Fatal("Fetch of an empty origin succeeded, want error")
	}
}

func TestHeadFailures(t *testing.T) {
	t.Run("HEAD names a missing branch", func(t *testing.T) {
		f := newFixture(t)
		f.head("gone")
		if _, err := f.open().Head(); err == nil || !strings.Contains(err.Error(), "resolving origin HEAD") {
			t.Fatalf("err = %v, want a HEAD resolution failure", err)
		}
	})
	t.Run("HEAD names a non-commit", func(t *testing.T) {
		f := newFixture(t)
		f.ref("refs/heads/blob", f.blob("not a commit\n"))
		f.head("blob")
		if _, err := f.open().Head(); err == nil || !strings.Contains(err.Error(), "reading HEAD commit") {
			t.Fatalf("err = %v, want a HEAD commit read failure", err)
		}
	})
}

// The advertised shape at storage level, exactly: only branch and tag
// hash refs appear, annotated tags gain one fully-peeled entry, an
// unpeelable tag gains none, and the output is name-sorted.
func TestRefsShapeAtStorageLevel(t *testing.T) {
	when := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	f := newFixture(t)
	c := f.commit("c", when)
	f.branch("main", c)
	f.head("main")
	f.annotatedTag("v1.0.0", c, plumbing.CommitObject, when)
	f.tag("light", c)
	// Sorts between v1.0.0's own entry and its `^{}` entry ('-' < '^'),
	// where storage iteration order — annotated tag immediately followed
	// by its peeled entry — would put it after both.
	f.tag("v1.0.0-pre", c)
	f.ref("refs/notes/commits", c)
	f.ref("refs/remotes/origin/main", c)
	f.symref("refs/heads/link", "refs/heads/main")
	corrupt := f.corruptObject(plumbing.TagObject, "not a decodable tag object")
	f.ref("refs/tags/corrupt", corrupt)

	tagRef, err := f.st.Reference(plumbing.ReferenceName("refs/tags/v1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	refs, err := f.open().Refs()
	if err != nil {
		t.Fatal(err)
	}
	want := []origin.Ref{
		{Name: "refs/heads/main", Hash: c.String()},
		{Name: "refs/tags/corrupt", Hash: corrupt.String()},
		{Name: "refs/tags/light", Hash: c.String()},
		{Name: "refs/tags/v1.0.0", Hash: tagRef.Hash().String()},
		{Name: "refs/tags/v1.0.0-pre", Hash: c.String()},
		{Name: "refs/tags/v1.0.0^{}", Hash: c.String()},
	}
	if !slices.Equal(refs, want) {
		t.Fatalf("Refs() = %+v, want %+v", refs, want)
	}
}

// A symbolic ref under refs/tags resolves through to its target, as it
// does for git itself — the resolved-lookup flag is what honors it.
// This pins lookup semantics at the storage level only: through the
// fetch seam the wire advertises every tag symref already resolved to
// its hash (HEAD alone stays symbolic), so fetched storage never holds
// one and Refs()' hash-refs-only view agrees with tag lookup there.
func TestResolveTagThroughSymbolicRef(t *testing.T) {
	when := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	f := newFixture(t)
	c := f.commit("c", when)
	f.branch("main", c)
	f.head("main")
	f.symref("refs/tags/v7.0.0", "refs/heads/main")
	got, err := f.open().ResolveVersion(mustParse(t, "v7.0.0"), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Hash != c.String() {
		t.Fatalf("resolved %s, want %s", got.Hash, c)
	}
}

func TestResolveTagPeelFailure(t *testing.T) {
	when := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	f := newFixture(t)
	c := f.commit("c", when)
	f.branch("main", c)
	f.head("main")
	f.ref("refs/tags/v1.0.0", f.corruptObject(plumbing.TagObject, "not a decodable tag object"))
	_, err := f.open().ResolveVersion(mustParse(t, "v1.0.0"), "")
	if err == nil || !strings.Contains(err.Error(), "peeling") {
		t.Fatalf("err = %v, want a peel failure", err)
	}
}

// Broken ref storage fails loudly on every path that reads refs — and
// never masquerades as an unknown version, which callers may treat as
// in-spec absence.
func TestCorruptPackedRefsFailsLoudly(t *testing.T) {
	when := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	f := newFixture(t)
	c := f.commit("c", when)
	f.branch("main", c)
	f.head("main")
	f.writeFile("packed-refs", "this is not a packed-refs file\n@@garbage@@\n")
	repo := f.open()

	if _, err := repo.ResolveVersion(mustParse(t, "v1.0.0"), ""); err == nil || errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("tag lookup err = %v, want a loud non-unknown failure", err)
	}
	if _, err := repo.Refs(); err == nil || !strings.Contains(err.Error(), "listing refs") {
		t.Fatalf("Refs() err = %v, want a listing failure", err)
	}
	v := mustPseudo(t, nil, when, c.String())
	if _, err := repo.ResolveVersion(v, ""); err == nil || !strings.Contains(err.Error(), "resolving "+v.String()) {
		t.Fatalf("pseudo err = %v, want base derivation to surface the corruption", err)
	}
}

func TestResolvePseudoCorruptCommitObject(t *testing.T) {
	when := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	f := newFixture(t)
	c := f.commit("c", when)
	f.branch("main", c)
	f.head("main")
	f.corruptObject(plumbing.CommitObject, "not a decodable commit object")
	v := mustPseudo(t, nil, when, c.String())
	_, err := f.open().ResolveVersion(v, "")
	if err == nil || !strings.Contains(err.Error(), "iterating commits") {
		t.Fatalf("err = %v, want a commit iteration failure", err)
	}
}

// Corruption inside the module's release-tag namespace fails base
// derivation loudly: a broken tag must never be silently treated as
// "no release", shrinking the history the base derives from — here the
// broken v5.0.0 would otherwise vanish and a v1.0.1-based pseudo would
// resolve.
func TestResolvePseudoCorruptReleaseTagFails(t *testing.T) {
	when := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	build := func(t *testing.T) (*repoFixture, version.Version) {
		f := newFixture(t)
		c1 := f.commit("c1", when)
		c2 := f.commit("c2", when.Add(time.Hour), c1)
		f.branch("main", c2)
		f.head("main")
		f.tag("v1.0.0", c1)
		v100 := mustParse(t, "v1.0.0")
		return f, mustPseudo(t, &v100, when.Add(time.Hour), c2.String())
	}
	t.Run("dangling tag target", func(t *testing.T) {
		f, v := build(t)
		f.annotatedTag("v5.0.0", plumbing.NewHash(strings.Repeat("cd", 20)), plumbing.CommitObject, when)
		_, err := f.open().ResolveVersion(v, "")
		if err == nil || !strings.Contains(err.Error(), "release tag v5.0.0") {
			t.Fatalf("err = %v, want a loud v5.0.0 corruption failure", err)
		}
	})
	t.Run("undecodable tag object", func(t *testing.T) {
		f, v := build(t)
		f.ref("refs/tags/v5.0.0", f.corruptObject(plumbing.TagObject, "not a decodable tag object"))
		_, err := f.open().ResolveVersion(v, "")
		if err == nil || !strings.Contains(err.Error(), "release tag v5.0.0") {
			t.Fatalf("err = %v, want a loud v5.0.0 corruption failure", err)
		}
	})
}

// A pseudo-version of a commit whose parent is missing from storage
// fails ancestry derivation loudly rather than shrinking the base.
func TestResolvePseudoDanglingParentFails(t *testing.T) {
	when := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	f := newFixture(t)
	x := f.commit("x", when, plumbing.NewHash(strings.Repeat("ab", 20)))
	f.branch("main", x)
	f.head("main")
	v := mustPseudo(t, nil, when, x.String())
	_, err := f.open().ResolveVersion(v, "")
	if err == nil || !strings.Contains(err.Error(), "reading ancestor") {
		t.Fatalf("err = %v, want an ancestor read failure", err)
	}
}

// The base derivation over arbitrary linear histories: for any chain
// with any subset of commits tagged by releases whose ordering is drawn
// independently of topology — so the highest tag on an ancestor need
// not be the nearest tagged ancestor — the expected pseudo-version of
// any commit resolves, and one crafted with a foreign base fails
// (REQ-resolve-pseudo-base).
func TestPseudoBaseAncestryProperty(t *testing.T) {
	v99 := mustParse(t, "v99.0.0")
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 6).Draw(rt, "commits")
		tagged := make([]bool, n)
		for i := range tagged {
			tagged[i] = rapid.Bool().Draw(rt, fmt.Sprintf("tagged%d", i))
		}
		minors := rapid.SliceOfNDistinct(rapid.IntRange(1, 50), n, n, rapid.ID).Draw(rt, "minors")
		target := rapid.IntRange(0, n-1).Draw(rt, "target")

		f := newFixture(rt)
		base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		hashes := make([]plumbing.Hash, n)
		var precedent *version.Version
		bestMinor := -1
		for i := range n {
			when := base.Add(time.Duration(i) * time.Hour)
			var parents []plumbing.Hash
			if i > 0 {
				parents = []plumbing.Hash{hashes[i-1]}
			}
			hashes[i] = f.commit(fmt.Sprintf("c%d", i), when, parents...)
			if tagged[i] {
				v := mustParse(rt, fmt.Sprintf("v0.%d.0", minors[i]))
				f.tag("prop/"+v.String(), hashes[i])
				if i <= target && minors[i] > bestMinor {
					bestMinor = minors[i]
					precedent = &v
				}
			}
		}
		f.branch("main", hashes[n-1])
		f.head("main")
		repo := f.fetch()

		when := base.Add(time.Duration(target) * time.Hour)
		expected := mustPseudo(rt, precedent, when, hashes[target].String())
		got, err := repo.ResolveVersion(expected, "prop")
		if err != nil {
			rt.Fatal(err)
		}
		if got.Hash != hashes[target].String() {
			rt.Fatal("resolved wrong commit: ", got.Hash)
		}

		crafted := mustPseudo(rt, &v99, when, hashes[target].String())
		if _, err := repo.ResolveVersion(crafted, "prop"); !errors.Is(err, ErrBaseInconsistent) {
			rt.Fatal("crafted base resolved: ", err)
		}
	})
}

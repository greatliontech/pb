package lsp

import (
	"fmt"
	"testing"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
	"pgregory.net/rapid"
)

// Over any sequence of judgements and document versions, the client
// holds exactly the current judgement's diagnostics and no stale one:
// what the publishes leave it with equals the judgement's lists, every
// file whose last publish carried a non-empty list is in each later
// judgement's publish set, and a member whose list and version did not
// move is not published again (REQ-lsp-fresh, REQ-lsp-diagnostics,
// the publish set term).
func TestPublishSetProperty(t *testing.T) {
	files := []uri.URI{"file:///t/a.proto", "file:///t/b.proto", "file:///t/pb.work", "pb-module://x/y%40v1/z.proto"}
	rapid.Check(t, func(rt *rapid.T) {
		s, err := New(Deps{})
		if err != nil {
			rt.Fatal(err)
		}
		client := map[uri.URI][]protocol.Diagnostic{} // what the client holds
		versions := map[uri.URI]int32{}
		rounds := rapid.IntRange(1, 8).Draw(rt, "rounds")
		for r := 0; r < rounds; r++ {
			lists := map[uri.URI][]protocol.Diagnostic{}
			docs := map[uri.URI]*document{}
			for i, u := range files {
				n := rapid.IntRange(0, 2).Draw(rt, fmt.Sprintf("r%d-f%d-count", r, i))
				for k := 0; k < n; k++ {
					lists[u] = append(lists[u], protocol.Diagnostic{
						Range:   protocol.Range{Start: protocol.Position{Line: uint32(rapid.IntRange(0, 3).Draw(rt, fmt.Sprintf("r%d-f%d-d%d-line", r, i, k)))}},
						Message: protocol.String(rapid.SampledFrom([]string{"one", "two"}).Draw(rt, fmt.Sprintf("r%d-f%d-d%d-msg", r, i, k))),
					})
				}
				if rapid.Bool().Draw(rt, fmt.Sprintf("r%d-f%d-open", r, i)) {
					if rapid.Bool().Draw(rt, fmt.Sprintf("r%d-f%d-moved", r, i)) {
						versions[u]++
					}
					docs[u] = &document{version: versions[u], proto: true}
				}
			}
			previous := map[uri.URI]publishState{}
			for u, st := range s.published {
				previous[u] = st
			}
			publishes := s.publishSet(lists, docs)
			seen := map[uri.URI]bool{}
			for _, p := range publishes {
				if seen[p.URI] {
					rt.Fatalf("round %d: %s published twice", r, p.URI)
				}
				seen[p.URI] = true
				client[p.URI] = p.Diagnostics
				if v, ok := p.Version.Get(); ok != (docs[p.URI] != nil) || ok && v != docs[p.URI].version {
					rt.Fatalf("round %d: %s published with version %v, document %v", r, p.URI, p.Version, docs[p.URI])
				}
				if prev, had := previous[p.URI]; had {
					enc, _ := protocol.Marshal(p.Diagnostics)
					if string(enc) == string(prev.list) && prev.open == (docs[p.URI] != nil) && (docs[p.URI] == nil || prev.version == docs[p.URI].version) {
						rt.Fatalf("round %d: %s published again unchanged", r, p.URI)
					}
				}
			}
			// Every file whose last publish was non-empty is a member:
			// either published now or, unchanged, still holding the
			// judgement's list.
			for u := range previous {
				if _, ok := lists[u]; !ok && !seen[u] {
					rt.Fatalf("round %d: %s held a list and was not withdrawn", r, u)
				}
			}
			// The client holds exactly the judgement's diagnostics.
			for u, want := range lists {
				got, _ := protocol.Marshal(client[u])
				w, _ := protocol.Marshal(want)
				if string(got) != string(w) {
					rt.Fatalf("round %d: the client holds %s for %s, the judgement placed %s", r, got, u, w)
				}
			}
			for u, held := range client {
				if _, placed := lists[u]; !placed && len(held) != 0 {
					rt.Fatalf("round %d: the client holds a stale list for %s: %d diagnostics", r, u, len(held))
				}
			}
		}
	})
}

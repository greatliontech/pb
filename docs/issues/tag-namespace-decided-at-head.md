# A module's tag namespace is decided at the origin's head alone

Lands: user decision — the namespace a subtree module's versions live
in decided once at the origin's default-branch head (the current
shape), or decided at each version's own commit; the choice moves
which versions resolve when a subtree's module file appears or
vanishes, and what a listing costs

The fetch client decides a subtree module's tag namespace once, from
the origin's default-branch head: subtree-prefixed tags where the
subtree holds a module file there, repository-level tags where it
exists holding none, the subtree's own namespace where it is absent
(fetch.Client.atHead; module-resolution.md
REQ-resolve-release-tags, REQ-resolve-synthesized-tags,
REQ-resolve-pseudo-base). The listing, a release's tag and a
pseudo-version's base all take that one decision, so a version named
in one namespace is fetched from the same and ranked against the same
releases. The spec states the judgment's commit — the head — and a
subtree can be declared at one commit and synthesized at another: a
module file added to a subtree that had releases as a synthesized
module, or removed from one that had releases in its own namespace.

The two defensible shapes:

- **Head decides** (current). One tree lookup per client, at a
  commit the snapshot store already holds. A subtree's namespace
  flips when its module file appears or vanishes at head, orphaning
  every release cut in the other namespace: a subtree declared at
  head loses the repository-level releases of its synthesized past,
  one undeclared at head loses its own tags.
- **Each version's commit decides.** A release tag names a version
  of the module iff, at the tagged commit, the tag's namespace
  matches the subtree's state there (a subtree tag over a declared
  subtree, a repository-level tag over a synthesized one); a
  pseudo-version's base takes the state at the embedded commit.
  Every release ever cut stays resolvable and a module's version set
  mixes two namespaces, ranked as one. The listing needs the tree at
  every candidate tag's commit — a tree fetch per tag against the
  history store's tree-less filter, proportional to the origin's tag
  count — or lists at head and lets an explicit version disagree
  with what the listing named.

Whichever shape stands, the spec names the commit the judgment is
made at; TestVersionsSubtreeDeletedAtHeadKeepsItsNamespace,
TestSynthesizedSubtreeRelease and TestSubtreePseudoBase pin the
current one.

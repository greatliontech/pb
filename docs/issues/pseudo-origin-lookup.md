# Pseudo-version origin lookup: absent-commit failure and base consistency

Lands: 9

Two clauses of the pseudo-version contract need commit-graph access that
only the git-backed fetch layer will have; the pure binding half
(`origin.VerifyPseudo`: embedded hash prefix + UTC commit time must match
the commit in hand) is implemented and enforced.

- **REQ-resolve-pseudo-commit, second clause**: "a pseudo-version naming
  a commit absent from the origin fails resolution." Requires resolving
  the embedded hash prefix at the origin; the fetch layer's lookup
  failure is the enforcement point. VerifyPseudo already refuses any
  commit that is not a full 40/64-digit lowercase-hex hash, so a caller
  cannot accidentally satisfy it with the version's own embedded prefix
  — but nothing yet performs the lookup.
- **REQ-resolve-pseudo-base**: the base must derive from the origin's
  release-tag history at the embedded commit (highest release tag on an
  ancestor, zero base when none). Requires ancestry over the commit
  graph. Without enforcement, a crafted `v99.0.0-0.<ts>-<hash>` naming a
  real commit passes VerifyPseudo and outranks every real release in
  minimal version selection.

- **REQ-resolve-synthesized-tags, head clause**: the fallback pseudo
  names "the origin's default-branch head commit".
  `origin.SynthesizedVersions` takes head caller-supplied; nothing yet
  resolves the origin's HEAD symref to that commit, so the clause has no
  enforcing symbol. The resolver of head is the same fetch layer.

All land where fetching materializes the commit graph: the direct
git-backed source. Resolution must resolve the default-branch head there
and call the two checks on every pseudo-version it materializes.

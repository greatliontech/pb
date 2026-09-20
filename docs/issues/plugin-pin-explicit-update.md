# A plugin pin has no explicit update

Lands: user decision

A module pin moves through `pb dep update`, the explicit user-invoked
rewrite REQ-lock-no-silent-downgrade sanctions. A plugin pin has no
such verb: it is created on first use, its tag never re-resolved
(REQ-plugin-digest-pin), and its provenance record judged on every
acquisition, so a record the evidence no longer bears refuses the run.
The only way to move one today is to change the reference's spelling
in generation configuration — a new tag is a fresh entry — or to edit
the lockfile by hand.

The fork:

- Extend `pb dep update` to plugin pins: a named reference is
  re-resolved and re-judged, its digest and record rewritten under
  the explicit-update sanction, so a re-signed image at a pinned
  digest, or a tag moved by its author, is reachable by the verb that
  moves every other pin.
- Leave plugin pins immutable: a plugin's identity is its reference
  as written, and moving it is rewriting the configuration; the
  refusal on a changed record is the design, and hand-editing the
  lockfile is the escape.

The externally visible tradeoff is whether a plugin whose signer or
tag legitimately changed is reachable through a verb, or only by
rewriting configuration or the lockfile.

# The lockfile's version is read as more spellings than the clause names

module-lockfile.md REQ-lock-format has `version` "the integer 1", and
the reader refuses a document of another integer version before it
judges anything else in it. The decode beneath it coerces, so `"1"`,
`'1'` and `1.0` are accepted as the integer 1 — spellings pb never
writes (REQ-lock-canonical-emission emits the plain integer) and the
clause does not admit. The fork: refuse the coerced spellings, so the
reader enforces the clause's exact words, at the cost of a document a
hand produced under YAML's own reading being refused; or amend the
clause to name the spellings the reader accepts, at the cost of a wire
contract wider than the emission. Both are defensible; the second
matches REQ-lock-acceptance's posture of accepting what re-emits to the
canonical form, the first the integer the clause names.

Lands: user decision.

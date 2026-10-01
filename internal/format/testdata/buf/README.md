# buf's formatter corpus

Every `.proto` here, with the `.golden` beside it, is copied from
`private/buf/bufformat/testdata` of github.com/bufbuild/buf at commit
cac04256faa3d6176133421d6f10ac2cdd9acb61, under the Apache License 2.0
in `LICENSE` beside this file. The pairs are the conformance anchor of
`docs/specs/format.md` (REQ-format-corpus): pb's formatter writes each
`.proto` as its `.golden`, byte for byte — but for the pairs named in
`EXCEPTIONS`, whose golden loses a comment of its input, buf's own
fault, and whose input pb pairs with its own golden under `../pb`.
Nothing here is edited; a form pb pins beyond these pairs lives under
`../pb`.

# connectrpc/rust generates against buffa, which the catalog does not carry

buf's connectrpc/rust plugin declares a dependency on
`buf.build/anthropics/buffa`: the service code protoc-gen-connect-rust
generates refers to the message types buffa generates, and a
`buf.gen.yaml` using connectrpc/rust names both. The plugin catalog's
membership (greatliontech/pb-plugins, README) mirrors buf's registry
by owner and does not include `anthropics`, so a migration of such a
file maps connectrpc/rust and reports buffa as an unmapped fact,
usable only through a replacement the user supplies; buffa itself
would fit the rust kind as it stands (the crate `protoc-gen-buffa`),
and buf's `anthropics/connect-rust` beside it.

Two defensible contracts:

1. The membership stays as it is: `anthropics` is no owner the
   catalog mirrors, and buffa comes by a replacement the user names.
2. The membership widens to buffa: the catalog commits to
   maintaining the plugins connectrpc/rust's output depends on, so
   the migrated file works whole.

The tradeoff is the user's: which plugins the catalog commits to
maintaining.

Lands: user decision.

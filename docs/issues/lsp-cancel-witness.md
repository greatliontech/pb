# The cancelled request's answer has no witness

`docs/specs/lsp.md` REQ-lsp-lifecycle: a request cancelled by the
client (`$/cancelRequest`) is answered `RequestCancelled` where its
work had not finished. The server's chain releases nothing — every
message is handled on the read loop in wire order (`internal/lsp`,
`Server.chain`) — so a cancellation is read only once the request it
names has been answered: the clause holds because its condition never
arises, and no test can witness the answer. The chain carries no
cancel observer for the same reason (one would cost each call its
context's materialization under the binding's INV-W for no effect).

Resolution: when a handler of the server first releases the read loop
(`jsonrpc2.Async`) — a request whose work outlasts the loop — the
chain gains the binding's cancel observer ahead of the typed dispatch,
and a test cancels such a request mid-work and sees `RequestCancelled`.

Lands: a server handler releases the read loop.

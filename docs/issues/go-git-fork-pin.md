# The go-git pin points at a fork until upstream carries the bound

Lands: go-git releases the bounded pack parser — the change on
github.com/thegrumpylion/go-git's bounded-index branch, proposed
upstream — and the pin moves to that release, the replace directive
dropped

go.mod replaces github.com/go-git/go-git/v6 with the fork's commit
c559ec92, upstream main plus the parser change that indexes a pack
within a delta base cache budget (module-proxy.md
REQ-proxy-direct-fetch: every fetch indexed within a memory bounded
independently of the pack's decoded size). Upstream's parser, run
without a storage to build a pack's index while the pack is written
to disk, held every delta stream and every resolved object until the
pack was parsed, and the whole fetch of a large history ended the
process. Until the change is upstream and released, the fork's
branch is rebased over upstream as upstream moves and the pin
follows it; pb's own tests witness the bound against whatever the
pin resolves to.

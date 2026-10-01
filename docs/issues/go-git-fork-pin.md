# The go-git pin points at a fork carrying the bounded pack parser

Lands: when git-go (greatliontech/git-go, the git library being
designed to serve pb's fetch) carries the fetch under
module-proxy.md REQ-proxy-direct-fetch — the go-git dependency and
its replace dropped together; the parser change is never proposed
upstream, the fork pinned until then

go.mod replaces github.com/go-git/go-git/v6 with the fork's main on
github.com/thegrumpylion/go-git, a tracking fork: upstream main plus
the parser change that indexes a pack within a delta base cache
budget, rebased over upstream as upstream moves and proposed
upstream as a pull request (module-proxy.md REQ-proxy-direct-fetch:
every fetch indexed within a memory bounded independently of the
pack's decoded size). Upstream's parser, run without a storage to
build a pack's index while the pack is written to disk, held every
delta stream and every resolved object until the pack was parsed,
and the whole fetch of a large history ended the process. Whichever
shape the fork takes, pb's own tests witness the bound against
whatever the pin resolves to (TestPackIndexBounded,
TestLiveWholeFetchBounded).

# The go-git pin points at a fork carrying the bounded pack parser

Lands: user decision — the fork's shape across the projects that
depend on go-git: a fork rebased over upstream as upstream moves,
the change proposed upstream and the pin moving to upstream's
release when it carries it; or a hard fork, the pin staying on it
for good

go.mod replaces github.com/go-git/go-git/v6 with the fork's commit
c559ec92 on github.com/thegrumpylion/go-git's bounded-index branch:
upstream main plus the parser change that indexes a pack within a
delta base cache budget (module-proxy.md REQ-proxy-direct-fetch:
every fetch indexed within a memory bounded independently of the
pack's decoded size). Upstream's parser, run without a storage to
build a pack's index while the pack is written to disk, held every
delta stream and every resolved object until the pack was parsed,
and the whole fetch of a large history ended the process. Whichever
shape the fork takes, pb's own tests witness the bound against
whatever the pin resolves to (TestPackIndexBounded,
TestLiveWholeFetchBounded).

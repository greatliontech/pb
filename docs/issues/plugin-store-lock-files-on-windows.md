# The plugin store's lock files persist on windows

pb's plugin store is an ocifs store under the user cache directory
(`pb/plugins`), and ocifs' lock library opens a claim's lock file
without delete sharing, so on windows a retired claim's file is
never unlinked: `pb/plugins/locks/` grows by one file per claim ever
made — a probe per store open, an op per acquisition or emptying, a
mount per id — and each lock-tier sweep re-retires every one of
them (ocifs' docs/issues/lock-files-persist-on-windows.md). No
verdict is wrong: a leftover is an unheld, acquirable dead entry.
The vcs store's lock file, which REQ-plat-files names, is a
different lock left in place on every platform by design.

Lands: when pb depends on an ocifs release whose
lock-files-persist-on-windows issue is closed (gmdb's lock opening
with delete sharing), a retired claim's file then absent on the
windows row.

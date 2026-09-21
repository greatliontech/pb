# A private origin cannot be fetched

Lands: the migrate plan's chunk 10

The fetch path carries no credentials: the direct construction
clones over HTTPS anonymously, and neither `module-resolution.md`,
`module-proxy.md` nor `user-config.md` speaks of a private origin.
A private ruleset (the published rules, private for now), a private
dependency, or a private origin for a workspace's own module fails
at fetch, and a migrated workspace naming a private ruleset fails at
the tidy that ends the verb. Go inherits the user's credential
helpers by invoking git; pb stays self-contained with go-git, so the
story is its own. Chunk 10 specifies two mechanisms and no other:
HTTPS credentials for a host read from the user's `.netrc` — an
origin's host or a proxy's, as Go reads them for a private proxy and
as every CI system knows how to write; and SSH for a `git@host:`
origin through the running agent.
Git's credential helpers are not invoked, since invoking them is
invoking git.

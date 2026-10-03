# grpc/node on darwin/arm64: upstream ships an x86_64 executable alone

The plugin catalog (greatliontech/pb-plugins) publishes grpc/node from
grpc-tools' prebuilt archives at grpc's binary host. The darwin-arm64
archive holds the same executable as the darwin-x64 one: a universal
binary carrying x86_64 alone, no arm64 slice, so a darwin/arm64 host
runs it under Rosetta or not at all. The catalog's release kind holds
every tree to an executable of the platform's architecture, by its
header, so grpc/node serves linux/amd64, linux/arm64, darwin/amd64
and windows/amd64 and not darwin/arm64, the architecture upstream
does not ship. buf's own images are Linux containers run on its
servers, so buf sets no precedent either way.

Two defensible contracts:

1. darwin/arm64 is not served, as now: the catalog publishes only
   what runs natively on the platform; a darwin/arm64 user has no
   grpc/node from the catalog until upstream ships an arm64
   executable (or builds one from source, a kind of its own).
2. darwin/arm64 is served with upstream's x86_64 executable: the
   release kind admits, for a darwin/arm64 tree, an executable of
   darwin/amd64 where the recipe says so, and the image runs on a
   host with Rosetta installed and fails without it; a user without
   Rosetta sees a plugin that will not start rather than one absent.

The tradeoff is the user's: a plugin absent for darwin/arm64, or one
present that needs Rosetta.

Lands: user decision.

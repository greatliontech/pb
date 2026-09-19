# Runner selection has no user-configuration layer

Lands: user decision (the home of pb's machine-scoped user
configuration)

plugin-execution.md's REQ-plugin-runner-selection ranks runner selection as the
`--runner` flag over `PBRUNNER` over user configuration over the
platform default. pb has no user configuration: every machine-scoped
setting so far is an environment variable (`PBPROXY`, `PBNOPROXY`,
`PBCACHE`, `PBTRUSTEDROOT`), and no spec names a configuration file,
its location, or its format. `plugrun.Open` applies the three
layers that exist and states this gap.

Settling it is a spec decision, not a runner one: whether pb gains a
user configuration file at all (a candidate home is the user's
configuration directory, `$XDG_CONFIG_HOME/pb`), which settings it
carries beside the runner, and how it ranks against the environment
for each. Until then the environment is the persistent machine-scoped
layer.

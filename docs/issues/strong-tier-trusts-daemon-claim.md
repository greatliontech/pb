# The Strong tier trusts the daemon's confinement claim

Lands: user decision

The docker runner derives the `Strong` tier from the container's
record as the daemon inspects it (plugin-execution.md
REQ-plugin-reported-tier), `apparmor unconfined` disqualifying. The
daemon writes that record from its own belief: moby sets
`AppArmorProfile` to `docker-default` wherever its system probe found
AppArmor's securityfs, while the profile is applied only where the
daemon's host check also passes — the parser at `/sbin/apparmor_parser`,
the kernel's `enabled` parameter `Y`, and the daemon itself not
contained. A daemon inside a container (docker-in-docker, the shape
of some CI), or on a host whose parser lives elsewhere, records
`docker-default` and runs the process unconfined; the tier reads
`Strong`, and the plugin's world is wider than the deviation list
says. The live suite catches it on such a daemon — the kernel's
report, read by the probe, disagrees with the daemon's claim — but a
user's run never runs the probe.

The fork, the user's to weigh:

- A preflight: the runner, at construction, runs a probe container
  once per session and reads the kernel's report for the process,
  deriving the tier's confinement from what the kernel says rather
  than what the daemon records; a container run per session, a few
  seconds, on every daemon.
- The claim stands: the tier trusts the daemon's record, as it trusts
  the record for every other fact (capabilities, namespaces, the
  seccomp profile), and a daemon that lies about its own confinement
  is a daemon that lies; the docker-in-docker case is documented as
  outside the tier.
- The tier grades confinement not at all: under moby the record reads
  `unconfined` only for a privileged container or one given an
  explicit security option, both of which the tier refuses on their
  own, so the `apparmor unconfined` rule fires nowhere today; dropping
  it states what holds — confinement narrows a plugin's world beyond
  what the tier promises and is a deviation, not a floor — at the
  cost of a daemon other than moby recording `unconfined` on its own
  and passing.

The tradeoff visible outside: the first costs every generation
session a probe run and makes the tier's verdict the kernel's; the
second keeps the runner cheap and the verdict the daemon's word; the
third makes the tier honest about what it grades and leaves a
non-moby daemon's confinement ungraded.

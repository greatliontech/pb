# The runner's spec re-spells the acquisition's image facts

Lands: package-structure plan chunk 9

The acquisition result carries an image's facts — the exported
rootfs, or the reference the daemon runs and whether it pulls it,
and the admitted platform — and the runner's spec carries the same
four, one renamed; the generate verb copies them field by field. One
fact, two shapes, a copy between them.

The result lives in a subpackage because it carries the lockfile's
pin, which no production code reads: the lockfile is the record, the
field a channel the acquirers' tests observe. Without the pin the
result needs only the process and the platform, both the domain
root's, so it belongs there beside them: plugin.Acquired and
plugin.Image, the runner's spec taking a *plugin.Image in place of
its four fields, the verb handing the result's image straight
through, the acquirers' tests reading the pin from the lockfile they
already hold. Nothing on the wire moves; the spec is the runner
seam's in-process argument.

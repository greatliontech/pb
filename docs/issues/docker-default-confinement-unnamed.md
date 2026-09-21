# The daemon's default confinement is not a named deviation

Lands: user decision

The docker runner's world beyond the image is specified as a list
of named deviations (plugin-execution.md REQ-plugin-sandboxed,
INV-docker-deviations): the runtime's filesystems and masks, the
daemon's name files, its injected variables. A daemon running under
its default AppArmor profile adds one the list does not name: the
profile refuses every write beneath `/proc` by path, whatever the
runtime's mask admits, and shapes other operations a plugin might
attempt. The runner already refuses `apparmor unconfined` as
disqualifying, so the profile is part of the tier's contract in one
direction and unnamed in the other; a host with AppArmor off, as the
development host has, shows a plugin a wider world than the CI
runner's daemon does.

The fork. One: name the default profile as a deviation and pin it
where the kernel has AppArmor on — the plugin's `/proc/self/attr/current`
reporting `docker-default (enforce)` — the deviation list then
complete for both hosts. Two: leave the daemon's confinement as its
unnamed contribution, the list naming what the runtime mounts and
injects alone. The tradeoff the user weighs: a platform-conditional
pin in the live suite, against a plugin author reading a deviation
list that omits the confinement that most often refuses them.

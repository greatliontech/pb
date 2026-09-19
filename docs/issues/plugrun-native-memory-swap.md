# The native runner's memory bound leaves swap unbounded

Lands: sandbox writes memory.swap.max beside memory.max (its cgroup
placement then bounds memory including swap)

Under cgroups the native runner's memory bound is sandbox's
`memory.max`; sandbox leaves `memory.swap.max` at its default, so on
a host with swap a plugin bounded to N bytes of memory may use swap
past it before the kernel kills it. The docker runner passes
`--memory-swap` equal to `--memory` and refuses a record that says
otherwise. The fix is sandbox's (the kernel rule lands in both
replica twins); pb's runner then reads the accounting as today.

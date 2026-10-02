# The Linux CPU-time kill's attribution under tick accounting

The sandbox attributes a Linux CPU-time kill by reading the dead
process's own CPU time from its zombie and holding it to the bound
within an allowance of 30 ms (its `docs/specs/sandbox.md`, "Bounded
means bounded"). The kernel fires `RLIMIT_CPU` on the thread group's
tick-sampled times — the process CPU clock's profiling sample, user
and system time charged a whole tick at a time — while the zombie's
`/proc/<pid>/stat` reports times rescaled to the scheduler's precise
runtime: two clocks that agree on an idle host and, under
contention, differ by sampling noise beyond the allowance, the more
so the coarser the tick. pb's spec verification leg, which executes
the runner suite under the whole corpus's parallel load on Ubuntu's
generic kernel (HZ=250, 4 ms ticks), saw the kill unattributed on
every try (runs 37044455399 and 37047606995, `TestRunCPUBound`),
while the live leg before it, run alone, saw it attributed; a host
with 1 ms ticks (HZ=1000, no `nohz_full` set, so tick-accounted
too) attributed it under synthetic load every time. pb's Linux arm
therefore accepts the uncounted reading, which still names the bound
as one of two causes, and Linux attribution has no live check in pb
until this lands; darwin's watchdog and the windows Job count their
own kills and are held to it.

Resolution: the sandbox reads the limit's own clock. The process CPU
clock of the zombie — `clock_gettime` on `MAKE_PROCESS_CPUCLOCK(pid,
CPUCLOCK_PROF)`, readable between the `waitid(WNOWAIT)` and the reap
exactly where the zombie's stat is read today — is the tick-sampled
sum the kernel compared against the limit, so the kill is attributed
exactly and the allowance for the two clocks' drift goes; the
allowance for the limit check's own tick stays. To be probed on a
loaded tick-accounting kernel (pb's spec verification leg is one)
before it replaces the reading. A proportional allowance would only
widen the window in which another hand's kill is misattributed; the
clock reading needs none.

Lands: a sandbox release reading the zombie's CPU time from its
process CPU clock is pb's dependency, and pb's Linux arm demands the
attribution again.

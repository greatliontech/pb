// Package flocktest asks whether a file's advisory lock is held, for
// a test that watches a lock taken and released by the code under
// test: the probe is a non-blocking exclusive attempt — flock on
// unix, LockFileEx on windows — on a descriptor of the test's own,
// released at once where it succeeds, so the question changes
// nothing of what it observes.
package flocktest

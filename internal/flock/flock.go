// Package flock takes a file's advisory lock without waiting: the
// platform's exclusive lock on an open file — flock on unix,
// LockFileEx on windows — asked for and refused at once where
// another descriptor holds it, so a holder can tell a live claim
// from a free one without blocking on it. The lock belongs to the
// open file description: it goes with the descriptor's close, is
// not inherited by a child process, and a second descriptor in the
// same process is refused like another process's.
package flock

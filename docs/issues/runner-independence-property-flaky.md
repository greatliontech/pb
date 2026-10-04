# The runner-independence property fails once and does not reproduce

`TestPropertyRunnerIndependence` (internal/plugin/runner/docker_live_test.go,
a live arm against the docker daemon) failed one whole-suite run with
rapid's "flaky test, can not reproduce a failure" under `-rapid.seed=22`:
the property's check failed on one draw and passed when rapid replayed
the same draw, so the failure depends on state the draw does not fix —
the daemon's, the host's, or an ordering between the two runners the
property compares. The rerun of the test alone passed. A property whose
failure is not a function of its inputs is either reading state outside
its inputs or racing it; which, the one failure did not say.

Lands: a failure of `TestPropertyRunnerIndependence` reproduced — the
reported seed replayed to a failing check, or the daemon's state at the
failure captured beside the rapid trace — so the state the property
reads outside its draw is named.

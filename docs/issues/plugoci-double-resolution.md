# Plugin acquisition resolves each image twice

Lands: ocifs exposes export from an already-pulled image (one
resolution yielding both the rootfs and the image config), and pb pins
that ocifs

`plugoci.Acquire` needs two things from the store — the exported root
filesystem and the image configuration (entrypoint, environment,
working directory) — and ocifs offers them through `Export` and `Pull`,
each of which resolves the reference and runs the verification seam.
Under pb's pull policy (pull if not present) the second call is served
from the store, so the two resolutions agree and no second network
round trip occurs; the seam runs twice, which is idempotent. The
duplication is structural, not behavioral: one store call returning
the image and its export would remove the second resolution and the
window a pull-always policy would open between them.

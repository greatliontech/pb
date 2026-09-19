# Plugin-acquisition mutation evidence is machine-local

Lands: mutation-evidence plan chunk 3

plugoci's tests exercise acquisition end to end through an in-memory
registry served over a loopback httptest socket, because ocifs
constructs its own registry transport and exposes no seam to inject an
in-process http.RoundTripper. gomutant classifies every plugoci mutant
unstable-oracle (the socket environment disqualifies attribution), so
the package's mutation records stay machine-local — the same class the
archive-extraction suite documents. The kill evidence exists (the
suite is genuinely adversarial); it cannot ride the repo document
until one side gains a seam.

# Discovery of cosign's carriers is proven on a test double

Lands: when gitprov's captured cosign vectors land — the same
capture's referrers list (the registry's answer and the fallback
tag's index) and signature-tag manifest are added under testdata and
discovery runs over them

Discovery (`internal/provenance/image/discover`) reads cosign's storage conventions:
the bundle referrer's artifact type and predicate annotation, the
legacy signature artifact's configuration media type, the
simple-signing layer media type and annotations, the signature tag.
Every test pushes carriers through `imagetest`, which spells those
conventions on its own from cosign's source and the OCI distribution
specification: a registry double answering the referrers API with the
manifest's artifact type and annotations, and the fallback tag kept
as cosign keeps it. No carrier cosign itself pushed, and no answer a
real registry gave, has been run through discovery. What a capture
settles: the referrer descriptor a real registry returns for cosign's
bundle artifact (artifact type from the manifest's own field, the
annotations copied), the fallback index cosign writes where the API
is absent, the exact layer annotations of a legacy signature, and
whether cosign's default sign still attaches the legacy tag beside
the bundle.

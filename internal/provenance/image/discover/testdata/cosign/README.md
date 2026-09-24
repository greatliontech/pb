# Captured cosign carriers

Every answer ghcr.io gave for an image cosign v3.1.3+dirty (a distribution build) signed keyless
twice, captured 2026-09-24 by the author with gitprov's
scripts/capture-cosign.sh (interactive Google OIDC; the leaves bind
the author's public identity, the entries are public in the
transparency log; no secret or third-party material). The image:
`ghcr.io/greatliontech/gitprov-cosign-capture@sha256:facb5564762d06aa0d30bba81be04b6d078cd289cb861e864dafd36a85322f28`,
a one-layer OCI image (`image-manifest.json`).

What the capture settled about cosign's storage conventions and a
real registry's answers:

- ghcr.io has no referrers API: `referrers.json` is its answer to
  `GET /v2/<repo>/referrers/<digest>`, `referrers.status` 404 and
  `referrers.headers` the response's headers.
- cosign's default sign wrote the bundle referrer and, the API being
  absent, the fallback tag `sha256-<hex>`:
  `referrers-fallback-tag.manifest.json`, an OCI index whose one
  descriptor names the referrer's media type, size, digest and
  artifact type and carries no annotations (go-containerregistry
  writes the tag so; a registry's API copies a manifest's
  annotations).
- The bundle referrer: `referrer-<hex>.manifest.json` with the
  artifact type `application/vnd.dev.sigstore.bundle.v0.3+json`, the
  annotations `dev.sigstore.bundle.content: dsse-envelope` and
  `dev.sigstore.bundle.predicateType: https://sigstore.dev/cosign/sign/v1`,
  an empty configuration (`.config.bin`) and the bundle as its one
  layer (`.layer-0.bin`).
- The default sign attached no legacy tag: after it, the tag
  `sha256-<hex>.sig` was absent (`after-bundle-sign.signature-tag.absent`,
  the registry's answer).
- The legacy sign (`--new-bundle-format=false`) wrote the tag:
  `signature-tag.manifest.json`, an OCI image manifest with an image
  configuration (`signature-tag.config.bin`) and one layer of media
  type `application/vnd.dev.cosign.simplesigning.v1+json`
  (`signature-tag.layer-0.bin`) whose annotations are exactly
  `dev.cosignproject.cosign/signature`,
  `dev.sigstore.cosign/certificate`, `dev.sigstore.cosign/chain`
  (empty) and `dev.sigstore.cosign/bundle`; no timestamp annotation.
- `trusted-root.json` is the trusted root in force at signing;
  `capture.json` the record (digest, identity, the bundle's entry).

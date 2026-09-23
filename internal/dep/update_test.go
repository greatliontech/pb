package dep

import (
	"testing"

	"github.com/greatliontech/pb/internal/module/lockfile"
)

// A record is reported as what vouched for it: an identity by its SAN
// and issuer, a pinned key by its kind and fingerprint, none as none.
func TestProvenanceSpelling(t *testing.T) {
	for want, rec := range map[string]lockfile.Provenance{
		"none": {},
		"git-signed-tag https://ci.example/wf by https://issuer.example":        {Type: lockfile.ProvenanceGitSignedTag, ObjectFormat: "sha1", Object: "ab", SAN: "https://ci.example/wf", Issuer: "https://issuer.example"},
		"image-signature https://ci.example/wf by https://issuer.example":       {Type: lockfile.ProvenanceImageSignature, SAN: "https://ci.example/wf", Issuer: "https://issuer.example"},
		"git-pinned-key ssh SHA256:cuQ/ZG8mqAef7X0GZ19RH5baTiwTVg76NePyXAKPBfM": {Type: lockfile.ProvenanceGitPinnedKey, ObjectFormat: "sha1", Object: "ab", KeyKind: "ssh", KeyFingerprint: "SHA256:cuQ/ZG8mqAef7X0GZ19RH5baTiwTVg76NePyXAKPBfM"},
		"git-pinned-key openpgp 91EDFEA1C6643EA64EC693516EA5914F2DADE816":       {Type: lockfile.ProvenanceGitPinnedKey, ObjectFormat: "sha1", Object: "ab", KeyKind: "openpgp", KeyFingerprint: "91EDFEA1C6643EA64EC693516EA5914F2DADE816"},
	} {
		if got := provenanceSpelling(rec); got != want {
			t.Errorf("provenanceSpelling(%+v) = %q, want %q", rec, got, want)
		}
	}
}

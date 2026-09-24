// Package imagetest serves registries and pushes signature carriers
// for tests of image-signature discovery: a registry that answers the
// OCI referrers API as the distribution specification describes it —
// a referrer's descriptor carrying the manifest's artifact type and
// annotations, a push of a manifest with a subject acknowledged — or
// one without the API, the fallback tag kept as cosign keeps it; and
// the pushes cosign's two conventions make, spelled here on their own
// so a test of discovery does not share discovery's constants.
package imagetest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/greatliontech/gitprov"
)

// cosign's storage conventions, as a test spells them.
const (
	BundleMediaType        = "application/vnd.dev.sigstore.bundle.v0.3+json"
	PredicateAnnotation    = "dev.sigstore.bundle.predicateType"
	ContentAnnotation      = "dev.sigstore.bundle.content"
	CosignSignPredicate    = "https://sigstore.dev/cosign/sign/v1"
	LegacyConfigMediaType  = "application/vnd.dev.cosign.artifact.sig.v1+json"
	SimpleSigningMediaType = "application/vnd.dev.cosign.simplesigning.v1+json"
	emptyConfigMediaType   = "application/vnd.oci.empty.v1+json"
	imageConfigMediaType   = "application/vnd.oci.image.config.v1+json"
	nonceAnnotation        = "test.pb.invalid/nonce"
)

var (
	manifestPath  = regexp.MustCompile(`^/v2/(.+)/manifests/([^/]+)$`)
	referrersPath = regexp.MustCompile(`^/v2/(.+)/referrers/([^/]+)$`)
)

// Handler is an in-memory registry tracking the manifests pushed with
// a subject, each as a descriptor carrying the manifest's artifact
// type — its artifactType, else its configuration media type — and
// annotations. With the referrers API it answers
// `/v2/<repo>/referrers/<digest>` with them and acknowledges a subject
// on push with the OCI-Subject header; without it, it serves them as
// the fallback tag's index as go-containerregistry writes the tag —
// the descriptors' annotations dropped — and the client's own upkeep
// of that tag is dropped.
func Handler(referrersAPI bool) http.Handler {
	inner := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	return &referrers{inner: inner, api: referrersAPI, by: map[string][]v1.Descriptor{}}
}

type referrers struct {
	inner http.Handler
	api   bool
	mu    sync.Mutex
	by    map[string][]v1.Descriptor // "<repo>@<subject digest>" -> referrers, push order
}

// fallbackTag is the referrers fallback tag's spelling.
var fallbackTag = regexp.MustCompile(`^sha256-[0-9a-f]{64}$`)

func (r *referrers) list(key string) []v1.Descriptor {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]v1.Descriptor(nil), r.by[key]...)
}

// serveIndex answers with an index of the descriptors: the referrers
// API's carry a manifest's annotations; the fallback tag's carry
// none, as go-containerregistry writes the tag.
func serveIndex(w http.ResponseWriter, list []v1.Descriptor, fallback bool) {
	if list == nil {
		list = []v1.Descriptor{}
	}
	if fallback {
		stripped := make([]v1.Descriptor, len(list))
		for i, d := range list {
			d.Annotations = nil
			stripped[i] = d
		}
		list = stripped
	}
	body, _ := json.Marshal(v1.IndexManifest{SchemaVersion: 2, MediaType: types.OCIImageIndex, Manifests: list})
	w.Header().Set("Content-Type", string(types.OCIImageIndex))
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

func (r *referrers) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if m := referrersPath.FindStringSubmatch(req.URL.Path); m != nil && req.Method == http.MethodGet && r.api {
		serveIndex(w, r.list(m[1]+"@"+m[2]), false)
		return
	}
	m := manifestPath.FindStringSubmatch(req.URL.Path)
	if m != nil && !r.api && fallbackTag.MatchString(m[2]) {
		key := m[1] + "@sha256:" + strings.TrimPrefix(m[2], "sha256-")
		switch req.Method {
		case http.MethodPut:
			// The client's upkeep of the fallback tag: dropped, the
			// tag is this registry's to serve.
			w.WriteHeader(http.StatusCreated)
			return
		case http.MethodGet, http.MethodHead:
			if list := r.list(key); len(list) > 0 {
				serveIndex(w, list, true)
				return
			}
		}
	}
	if m == nil || req.Method != http.MethodPut {
		r.inner.ServeHTTP(w, req)
		return
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.Body = io.NopCloser(bytes.NewReader(raw))
	var mf struct {
		ArtifactType string                     `json:"artifactType"`
		Config       struct{ MediaType string } `json:"config"`
		Subject      *struct{ Digest string }   `json:"subject"`
		Annotations  map[string]string          `json:"annotations"`
	}
	_ = json.Unmarshal(raw, &mf)
	if mf.Subject == nil {
		r.inner.ServeHTTP(w, req)
		return
	}
	sum := sha256.Sum256(raw)
	d := v1.Descriptor{
		MediaType:    types.MediaType(req.Header.Get("Content-Type")),
		Size:         int64(len(raw)),
		Digest:       v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(sum[:])},
		ArtifactType: mf.ArtifactType,
		Annotations:  mf.Annotations,
	}
	if d.ArtifactType == "" {
		d.ArtifactType = mf.Config.MediaType
	}
	ack := &acknowledging{ResponseWriter: w, subject: mf.Subject.Digest, api: r.api}
	r.inner.ServeHTTP(ack, req)
	if ack.status == http.StatusCreated {
		key := m[1] + "@" + mf.Subject.Digest
		r.mu.Lock()
		if !hasDigest(r.by[key], d.Digest) {
			r.by[key] = append(r.by[key], d)
		}
		r.mu.Unlock()
	}
}

func hasDigest(list []v1.Descriptor, h v1.Hash) bool {
	for _, d := range list {
		if d.Digest == h {
			return true
		}
	}
	return false
}

// acknowledging adds the OCI-Subject header to a created manifest's
// response, the registry's word that it tracks the referrer.
type acknowledging struct {
	http.ResponseWriter
	subject string
	api     bool
	status  int
}

func (a *acknowledging) WriteHeader(status int) {
	a.status = status
	if status == http.StatusCreated && a.api {
		a.Header().Set("OCI-Subject", a.subject)
	}
	a.ResponseWriter.WriteHeader(status)
}

// Registry serves Handler over loopback for the test and returns a
// repository on it.
func Registry(t testing.TB, referrersAPI bool) name.Repository {
	t.Helper()
	srv := httptest.NewServer(Handler(referrersAPI))
	t.Cleanup(srv.Close)
	repo, err := name.NewRepository(srv.Listener.Addr().String() + "/org/plugin")
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// PushIndex pushes a small manifest list under tag and returns its
// digest, the subject signatures attach to.
func PushIndex(t testing.TB, repo name.Repository, tag string, opts ...remote.Option) v1.Hash {
	t.Helper()
	idx, err := random.Index(64, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(repo.Tag(tag), idx, opts...); err != nil {
		t.Fatal(err)
	}
	h, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// Layer is one layer of a signature artifact: its bytes, media type
// and annotations.
type Layer struct {
	MediaType   string
	Content     []byte
	Annotations map[string]string
}

// Artifact is a referrer to push: its artifact type, its annotations
// and its layers, the configuration the empty one cosign writes.
type Artifact struct {
	ArtifactType string
	Annotations  map[string]string
	Layers       []Layer
}

// Order places a referrer's digest relative to another's, for a test
// of the order carriers are taken in.
type Order int

const (
	// Anywhere leaves the digest to chance.
	Anywhere Order = iota
	// Before is a digest sorting before the pivot's; After one
	// sorting after it.
	Before
	After
)

// Attach pushes a as a referrer of subject in repo — the manifest
// naming the subject, its artifact type and annotations — and returns
// its digest.
func Attach(t testing.TB, repo name.Repository, subject v1.Hash, a Artifact, opts ...remote.Option) v1.Hash {
	t.Helper()
	return AttachOrdered(t, repo, subject, a, Anywhere, v1.Hash{}, opts...)
}

// AttachOrdered is Attach with the referrer's digest placed before or
// after pivot's, a nonce annotation varied until it is.
func AttachOrdered(t testing.TB, repo name.Repository, subject v1.Hash, a Artifact, order Order, pivot v1.Hash, opts ...remote.Option) v1.Hash {
	t.Helper()
	subjectDesc, err := remote.Head(repo.Digest(subject.String()), opts...)
	if err != nil {
		t.Fatal(err)
	}
	for nonce := 0; ; nonce++ {
		mf, blobs := manifestOf(a)
		mf.Subject = &v1.Descriptor{MediaType: subjectDesc.MediaType, Size: subjectDesc.Size, Digest: subjectDesc.Digest}
		if order != Anywhere {
			if mf.Annotations == nil {
				mf.Annotations = map[string]string{}
			}
			mf.Annotations[nonceAnnotation] = fmt.Sprint(nonce)
		}
		m := rawManifest(t, mf)
		cmp := strings.Compare(m.desc.Digest.String(), pivot.String())
		if order == Anywhere || (order == Before && cmp < 0) || (order == After && cmp > 0) {
			return put(t, repo, m, blobs, "", opts)
		}
		// A try lands on the wanted side with the pivot's own share
		// of the digest space; the cap leaves a pivot within a
		// hundred-thousandth of either end as the one that fails.
		if nonce > 1<<16 {
			t.Fatalf("%d nonces gave no digest on the wanted side of %s", nonce, pivot)
		}
	}
}

// Tag pushes a signature image of layers under tag with no subject,
// cosign's legacy location.
func Tag(t testing.TB, repo name.Repository, tag string, layers []Layer, opts ...remote.Option) v1.Hash {
	t.Helper()
	mf, blobs := manifestOf(Artifact{ArtifactType: imageConfigMediaType, Layers: layers})
	mf.ArtifactType = ""
	return put(t, repo, rawManifest(t, mf), blobs, tag, opts)
}

// manifestOf builds the manifest naming the layers and the empty
// configuration, and the blobs to upload with it.
func manifestOf(a Artifact) (*v1.Manifest, []v1.Layer) {
	config := static.NewLayer([]byte("{}"), emptyConfigMediaType)
	switch a.ArtifactType {
	case LegacyConfigMediaType:
		config = static.NewLayer([]byte("{}"), LegacyConfigMediaType)
	case imageConfigMediaType:
		config = static.NewLayer([]byte("{}"), imageConfigMediaType)
	}
	mf := &v1.Manifest{SchemaVersion: 2, MediaType: types.OCIManifestSchema1, Annotations: cloneMap(a.Annotations)}
	if a.ArtifactType != LegacyConfigMediaType && a.ArtifactType != imageConfigMediaType {
		mf.ArtifactType = a.ArtifactType
	}
	blobs := []v1.Layer{config}
	mf.Config = describe(config)
	for _, l := range a.Layers {
		layer := static.NewLayer(l.Content, types.MediaType(l.MediaType))
		d := describe(layer)
		d.Annotations = cloneMap(l.Annotations)
		mf.Layers = append(mf.Layers, d)
		blobs = append(blobs, layer)
	}
	return mf, blobs
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func describe(l v1.Layer) v1.Descriptor {
	h, _ := l.Digest()
	size, _ := l.Size()
	mt, _ := l.MediaType()
	return v1.Descriptor{MediaType: mt, Size: size, Digest: h}
}

// rawManifest serializes a manifest and describes it as a referrers
// list would: its artifact type and annotations.
func rawManifest(t testing.TB, mf *v1.Manifest) *manifest {
	t.Helper()
	raw, err := json.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	artifactType := mf.ArtifactType
	if artifactType == "" {
		artifactType = string(mf.Config.MediaType)
	}
	return &manifest{raw: raw, desc: v1.Descriptor{
		MediaType: mf.MediaType, Size: int64(len(raw)),
		Digest:       v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(sum[:])},
		ArtifactType: artifactType, Annotations: cloneMap(mf.Annotations),
	}}
}

// put uploads the blobs and pushes the manifest at tag, or at its
// digest when tag is empty.
func put(t testing.TB, repo name.Repository, m *manifest, blobs []v1.Layer, tag string, opts []remote.Option) v1.Hash {
	t.Helper()
	for _, l := range blobs {
		if err := remote.WriteLayer(repo, l, opts...); err != nil {
			t.Fatal(err)
		}
	}
	var ref name.Reference = repo.Digest(m.desc.Digest.String())
	if tag != "" {
		ref = repo.Tag(tag)
	}
	if err := remote.Put(ref, m, opts...); err != nil {
		t.Fatal(err)
	}
	return m.desc.Digest
}

// manifest is a raw manifest the client can push, describing itself
// with its artifact type and annotations so the fallback tag the
// client keeps carries them.
type manifest struct {
	raw  []byte
	desc v1.Descriptor
}

func (m *manifest) RawManifest() ([]byte, error)        { return m.raw, nil }
func (m *manifest) MediaType() (types.MediaType, error) { return m.desc.MediaType, nil }
func (m *manifest) Digest() (v1.Hash, error)            { return m.desc.Digest, nil }
func (m *manifest) Size() (int64, error)                { return m.desc.Size, nil }
func (m *manifest) Descriptor() (*v1.Descriptor, error) { d := m.desc; return &d, nil }
func (m *manifest) ArtifactType() (string, error)       { return m.desc.ArtifactType, nil }

// BundleArtifact is the referrer cosign's default sign pushes for a
// bundle: the bundle's media type as artifact type and layer, the
// content and predicate annotations.
func BundleArtifact(b gitprov.SigstoreBundle, predicate string) Artifact {
	anns := map[string]string{ContentAnnotation: "dsse-envelope", PredicateAnnotation: predicate}
	return Artifact{ArtifactType: BundleMediaType, Annotations: anns, Layers: []Layer{{MediaType: BundleMediaType, Content: b.JSON, Annotations: anns}}}
}

// EnvelopeLayer is the signature layer cosign's legacy sign writes
// for an envelope: the payload as content, the parts as annotations,
// absent ones omitted.
func EnvelopeLayer(e gitprov.SimpleSigningEnvelope) Layer {
	anns := map[string]string{"dev.cosignproject.cosign/signature": e.Signature, "dev.sigstore.cosign/certificate": e.Certificate}
	for k, v := range map[string]string{"dev.sigstore.cosign/chain": e.Chain, "dev.sigstore.cosign/bundle": e.RekorBundle, "dev.sigstore.cosign/rfc3161timestamp": e.RFC3161Timestamp} {
		if v != "" {
			anns[k] = v
		}
	}
	return Layer{MediaType: SimpleSigningMediaType, Content: e.Payload, Annotations: anns}
}

// SignatureTag is cosign's legacy tag for a digest's signatures.
func SignatureTag(digest v1.Hash) string { return digest.Algorithm + "-" + digest.Hex + ".sig" }

// Replay serves a captured registry's answers verbatim, for a test
// over bytes a real registry gave: each manifest under the digest or
// tag it was fetched by, with the media type its own bytes name and
// its digest; each blob under its digest; and the referrers API
// answered with the captured status and body for every digest. A
// request for anything else is answered 404 as a registry answers
// it. The repository is the captured one's path on the test server.
func Replay(t testing.TB, repoPath string, manifests map[string][]byte, blobs [][]byte, referrersStatus int, referrersBody []byte) name.Repository {
	t.Helper()
	byDigest := map[string][]byte{}
	for _, b := range blobs {
		byDigest[Digest(b)] = b
	}
	mediaTypes := map[string]string{}
	for key, m := range manifests {
		var mf struct {
			MediaType string `json:"mediaType"`
		}
		if err := json.Unmarshal(m, &mf); err != nil || mf.MediaType == "" {
			t.Fatalf("replay: manifest %s names no media type: %v", key, err)
		}
		mediaTypes[key] = mf.MediaType
	}
	prefix := "/v2/" + repoPath + "/"
	notFound := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, under := strings.CutPrefix(r.URL.Path, prefix)
		switch {
		case r.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)
		case !under:
			notFound(w)
		case strings.HasPrefix(rest, "referrers/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(referrersStatus)
			w.Write(referrersBody)
		case strings.HasPrefix(rest, "manifests/"):
			key := strings.TrimPrefix(rest, "manifests/")
			m, ok := manifests[key]
			if !ok {
				notFound(w)
				return
			}
			w.Header().Set("Content-Type", mediaTypes[key])
			w.Header().Set("Docker-Content-Digest", Digest(m))
			w.Header().Set("Content-Length", fmt.Sprint(len(m)))
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				w.Write(m)
			}
		case strings.HasPrefix(rest, "blobs/"):
			b, ok := byDigest[strings.TrimPrefix(rest, "blobs/")]
			if !ok {
				notFound(w)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(b)))
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				w.Write(b)
			}
		default:
			notFound(w)
		}
	}))
	t.Cleanup(srv.Close)
	repo, err := name.NewRepository(srv.Listener.Addr().String() + "/" + repoPath)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// Digest is the sha256 digest of bytes as a registry spells it.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

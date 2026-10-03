package runner

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/plugin/oci"
	"github.com/greatliontech/pb/internal/plugin/runner/testdata/behavior"
	"github.com/greatliontech/pb/internal/provenance/trust"
)

// The store byte path end to end: the fake plugin published to a
// registry as an image under a manifest list, acquired through the
// seam into pb's store, and run under the daemon from the archive
// the store writes of it — the registry's compressed layer and the
// image's own configuration — the daemon's reported identity held
// to the archive's (REQ-plugin-core-verifies). Runs where a daemon
// answers; demanded under PB_TEST_REQUIRE_DOCKER.
func TestDockerLiveLoadsStoreArchive(t *testing.T) {
	r := requireDaemon(t)
	var layer bytes.Buffer
	if err := layerTar(&layer, rootfsDir); err != nil {
		t.Fatal(err)
	}
	l, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(layer.Bytes())), nil })
	if err != nil {
		t.Fatal(err)
	}
	img, err := mutate.AppendLayers(empty.Image, l)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cf = cf.DeepCopy()
	cf.OS, cf.Architecture = r.Platform().OS, r.Platform().Arch
	cf.Config.Entrypoint = []string{"/plugin"}
	cf.Config.Env = []string{"PB_PLUGIN_TEST_ENV=from-the-image"}
	if img, err = mutate.ConfigFile(img, cf); err != nil {
		t.Fatal(err)
	}
	idx := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: cf.OS, Architecture: cf.Architecture}}})
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer srv.Close()
	ref := srv.Listener.Addr().String() + "/org/plugin:v1"
	tag, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(tag, idx); err != nil {
		t.Fatal(err)
	}
	a, err := oci.New(oci.Config{WorkDir: t.TempDir(), Lock: &lockfile.File{}, Policy: &trust.Policy{}, Transport: http.DefaultTransport})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	acq, err := a.Acquire(ctx, ref, []oci.Candidate{{Platform: r.Platform()}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(ctx, Spec{Scheme: plugin.SchemeOCI, Image: acq.Image, Process: acq.Process, Stdin: request(t, behavior.Env), Limits: limits(nil), MinTier: plugin.TierStrong})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || res.Tier != plugin.TierStrong {
		t.Fatalf("result = %+v", res)
	}
	// The image's own configuration runs: its environment reaches
	// the plugin, as the daemon applies it.
	if got := content(t, res); got != "PB_PLUGIN_TEST_ENV=from-the-image" {
		t.Fatalf("the plugin answered %q under the image's own environment", got)
	}
}

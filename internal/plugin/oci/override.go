package oci

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/greatliontech/pb/internal/plugin"
)

// reservedDomain is the domain the acquirer's in-process hosts are
// named under: reserved (RFC 2606), so no resolver answers them and
// a round trip for one that escaped the acquirer's transport fails
// rather than reaches a host.
const reservedDomain = ".pb.invalid"

// stagingHost names the override staging; stagingRepo is the
// repository overrides are staged under.
const (
	stagingHost = "override" + reservedDomain
	stagingRepo = stagingHost + "/override"
)

// newStagingRegistry returns the acquirer's registry for overrides,
// the handler its transport serves in this process at stagingHost:
// content pb read from the invocation's own files reaches the store
// by the one byte path the store verifies, and nothing else reads
// what is staged or pushes into it.
func newStagingRegistry() http.Handler {
	return registry.New(registry.Logger(log.New(io.Discard, "", 0)))
}

// inProcessTransport serves round trips for the hosts it holds by
// their handlers in this process — no socket is bound — and every
// other by base; with no base, a host it does not hold is refused,
// so a suite built on it dials nothing. A response is buffered
// whole, so a staged blob is held in memory as it is read where a
// socket would stream it, and a HEAD's carries the content length
// beside an empty body, which the registry client reads by header.
type inProcessTransport struct {
	base     http.RoundTripper
	mu       sync.Mutex
	handlers map[string]http.Handler
}

func newInProcessTransport(base http.RoundTripper) *inProcessTransport {
	return &inProcessTransport{base: base, handlers: map[string]http.Handler{}}
}

// serve holds h for host; a nil h releases the host.
func (t *inProcessTransport) serve(host string, h http.Handler) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if h == nil {
		delete(t.handlers, host)
		return
	}
	t.handlers[host] = h
}

func (t *inProcessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	h, ok := t.handlers[req.URL.Host]
	t.mu.Unlock()
	if !ok {
		if t.base == nil {
			if req.Body != nil {
				req.Body.Close()
			}
			return nil, fmt.Errorf("%s: no handler in this process and no transport beyond it", req.URL.Host)
		}
		return t.base.RoundTrip(req)
	}
	// The round tripper's contract: the caller's request is not
	// mutated and its body is closed. The handler is served a copy
	// with the non-nil body the wire would give it.
	served := req.Clone(req.Context())
	if served.Body == nil {
		served.Body = http.NoBody
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, served)
	if req.Body != nil {
		req.Body.Close()
	}
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

// AcquireOverride materializes an override for the entry declared as
// ref from source — a directory holding an OCI layout, or a file
// holding an OCI layout archive or a docker-save tarball — leaving
// the lockfile untouched in both directions: no pin is written and
// the entry's pin is not consulted (REQ-plugin-override). The content
// is staged on the acquirer's registry in this process and pulled
// through the seam by digest, which runs the platform check and the trust
// policy against the declared reference exactly as for any
// acquisition (REQ-plugin-verify-before-run, REQ-plugin-core-verifies).
func (a *Acquirer) AcquireOverride(ctx context.Context, ref, source string, candidates []Candidate) (*plugin.Acquired, error) {
	idx, release, err := loadOverride(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("oci: override %s for %s: %w", source, ref, err)
	}
	defer release()
	idx, err = withPlatforms(idx)
	if err != nil {
		return nil, fmt.Errorf("oci: override %s for %s: %w", source, ref, err)
	}
	digest, err := idx.Digest()
	if err != nil {
		return nil, fmt.Errorf("oci: override %s for %s: %w", source, ref, err)
	}
	tag, err := name.ParseReference(stagingRepo + ":override")
	if err != nil {
		return nil, err
	}
	if err := remote.WriteIndex(tag, idx, remote.WithContext(ctx), remote.WithTransport(a.transport)); err != nil {
		return nil, fmt.Errorf("oci: override %s for %s: staging: %w", source, ref, err)
	}
	target := stagingRepo + "@" + digest.String()
	// The staging is this process's registry, which no daemon
	// reaches: every candidate is served from the store, the daemon's
	// runner importing the export as it does any.
	stored := make([]Candidate, len(candidates))
	for i, c := range candidates {
		stored[i] = Candidate{Platform: c.Platform}
	}
	acq := &acquisition{declaredRef: ref, candidates: stored}
	if err := a.enter(target, acq); err != nil {
		return nil, err
	}
	defer a.leave(target)
	acquired, err := a.acquire(ctx, target, acq)
	if err != nil {
		return nil, err
	}
	if acq.resolved == "" {
		return nil, fmt.Errorf("oci: override %s for %s: acquisition ran no verification seam", source, ref)
	}
	return acquired, nil
}

// withPlatforms fills in, for every image the manifest list carries
// without a platform, the platform its configuration names: a
// docker-save archive (an OCI layout since Docker 25) and a
// single-image layout list none, and the seam matches platforms as
// listed (REQ-plugin-platform-strict).
func withPlatforms(idx v1.ImageIndex) (v1.ImageIndex, error) {
	manifest, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	filled := v1.ImageIndex(empty.Index)
	for _, d := range manifest.Manifests {
		if d.Platform != nil && d.Platform.OS != "" && d.Platform.Architecture != "" {
			if d.MediaType.IsIndex() {
				child, err := idx.ImageIndex(d.Digest)
				if err != nil {
					return nil, err
				}
				filled = mutate.AppendManifests(filled, mutate.IndexAddendum{Add: child, Descriptor: d})
				continue
			}
			img, err := idx.Image(d.Digest)
			if err != nil {
				return nil, err
			}
			filled = mutate.AppendManifests(filled, mutate.IndexAddendum{Add: img, Descriptor: d})
			continue
		}
		if d.MediaType.IsIndex() {
			return nil, fmt.Errorf("the manifest list nests a list without a platform (%s)", d.Digest)
		}
		img, err := idx.Image(d.Digest)
		if err != nil {
			return nil, err
		}
		cfg, err := img.ConfigFile()
		if err != nil {
			return nil, err
		}
		if cfg.OS == "" || cfg.Architecture == "" {
			return nil, fmt.Errorf("image %s names no platform in its configuration and none in the manifest list", d.Digest)
		}
		d.Platform = &v1.Platform{OS: cfg.OS, Architecture: cfg.Architecture, Variant: cfg.Variant}
		filled = mutate.AppendManifests(filled, mutate.IndexAddendum{Add: img, Descriptor: d})
	}
	return filled, nil
}

// loadOverride reads an override source into a manifest list: an OCI
// layout directory as it is; a file as an OCI layout archive, or
// failing that a legacy docker-save tarball, whose single image is
// listed as it is. The index reads its blobs lazily, so an unpacked
// archive lives until release is called.
func loadOverride(ctx context.Context, source string) (idx v1.ImageIndex, release func(), err error) {
	release = func() {}
	fi, err := os.Stat(source)
	if err != nil {
		return nil, release, err
	}
	if fi.IsDir() {
		idx, err := layout.ImageIndexFromPath(source)
		if err != nil {
			return nil, release, fmt.Errorf("not an OCI layout: %w", err)
		}
		return idx, release, nil
	}
	dir, err := os.MkdirTemp("", "pb-override-*")
	if err != nil {
		return nil, release, err
	}
	release = func() { os.RemoveAll(dir) }
	untarErr := untar(ctx, source, dir)
	if untarErr == nil {
		idx, layoutErr := layout.ImageIndexFromPath(dir)
		if layoutErr == nil {
			return idx, release, nil
		}
		untarErr = layoutErr
	}
	img, err := tarball.ImageFromPath(source, nil)
	if err != nil {
		return nil, release, fmt.Errorf("neither an OCI layout archive (%v) nor a docker-save tarball (%v)", untarErr, err)
	}
	return mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: img}), release, nil
}

// untar extracts a tar archive of regular files and directories into
// dir, refusing any entry that would land outside it and any other
// kind of entry — a layout holds files and directories only.
func untar(ctx context.Context, archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// A member's name is local to the layout as the host reads
		// it: no parent step, no volume, no rooted spelling, on any
		// platform's separators (filepath.IsLocal).
		if !filepath.IsLocal(filepath.FromSlash(h.Name)) {
			return fmt.Errorf("archive entry %q escapes the layout", h.Name)
		}
		target := filepath.Join(dir, filepath.FromSlash(h.Name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		default:
			return fmt.Errorf("archive entry %q is neither a file nor a directory", h.Name)
		}
	}
}

package plugoci

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// staging is the acquirer's loopback registry for overrides: content
// pb read from the invocation's own files reaches the store by the
// one byte path the store verifies. It answers only the store's
// requests — a bearer token minted at construction, held by the
// store's keychain for this repository and by nothing else — so no
// other local process reads what is staged or pushes into it.
type staging struct {
	srv   *http.Server
	host  string
	token string
	repo  string
}

// stagingRepository is the loopback repository overrides are staged
// under.
const stagingRepository = "override"

// listenStaging binds the staging registry's loopback port; tests
// point it at a host that refuses.
var listenStaging = func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }

func newStaging() (*staging, error) {
	l, err := listenStaging()
	if err != nil {
		return nil, fmt.Errorf("override staging: %w", err)
	}
	var tok [24]byte
	rand.Read(tok[:])
	s := &staging{host: l.Addr().String(), token: hex.EncodeToString(tok[:])}
	s.repo = s.host + "/" + stagingRepository
	inner := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	s.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The version ping is open, as every client sends it before
		// authenticating; everything else carries the token.
		if r.URL.Path != "/v2/" && r.Header.Get("Authorization") != "Bearer "+s.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	})}
	go s.srv.Serve(l)
	return s, nil
}

func (s *staging) auth() authn.AuthConfig { return authn.AuthConfig{RegistryToken: s.token} }

func (s *staging) close() error { return s.srv.Close() }

// AcquireOverride materializes an override for the entry declared as
// ref from source — a directory holding an OCI layout, or a file
// holding an OCI layout archive or a docker-save tarball — leaving
// the lockfile untouched in both directions: no pin is written and
// the entry's pin is not consulted (REQ-plugin-override). The content
// is staged on the acquirer's loopback registry and pulled through
// the seam by digest, which runs the platform check and the trust
// policy against the declared reference exactly as for any
// acquisition (REQ-plugin-verify-before-run, REQ-plugin-core-verifies).
func (a *Acquirer) AcquireOverride(ctx context.Context, ref, source string) (*Acquired, error) {
	if a.staging == nil {
		return nil, fmt.Errorf("plugoci: override %s for %s: %w", source, ref, a.stagingErr)
	}
	idx, release, err := loadOverride(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("plugoci: override %s for %s: %w", source, ref, err)
	}
	defer release()
	idx, err = withPlatforms(idx)
	if err != nil {
		return nil, fmt.Errorf("plugoci: override %s for %s: %w", source, ref, err)
	}
	digest, err := idx.Digest()
	if err != nil {
		return nil, fmt.Errorf("plugoci: override %s for %s: %w", source, ref, err)
	}
	tag, err := name.ParseReference(a.staging.repo + ":override")
	if err != nil {
		return nil, err
	}
	if err := remote.WriteIndex(tag, idx, remote.WithContext(ctx), remote.WithAuth(authn.FromConfig(a.staging.auth()))); err != nil {
		return nil, fmt.Errorf("plugoci: override %s for %s: staging: %w", source, ref, err)
	}
	target := a.staging.repo + "@" + digest.String()
	acq := &acquisition{declaredRef: ref}
	if err := a.enter(target, acq); err != nil {
		return nil, err
	}
	defer a.leave(target)
	img, err := a.fs.Pull(ctx, target)
	if err != nil {
		return nil, err
	}
	rootfs, err := img.Export(ctx)
	if err != nil {
		return nil, err
	}
	if acq.resolved == "" {
		return nil, fmt.Errorf("plugoci: override %s for %s: acquisition ran no verification seam", source, ref)
	}
	process, err := processOf(img.ConfigFile())
	if err != nil {
		return nil, fmt.Errorf("plugoci: override %s for %s: %v", source, ref, err)
	}
	return &Acquired{Rootfs: rootfs, Process: process}, nil
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
		target := filepath.Join(dir, filepath.FromSlash(h.Name))
		if rel, err := filepath.Rel(dir, target); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return fmt.Errorf("archive entry %q escapes the layout", h.Name)
		}
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

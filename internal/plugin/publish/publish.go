// Package publish builds and publishes plugin images
// (plugin-publish.md): one image per platform over a platform tree,
// optionally over a base, and the manifest list over exactly the
// platforms given, pushed to a full reference under the publisher's
// own credentials. It runs no container engine: the layer is a tar of
// the tree written in canonical form, the image and the list are
// assembled in process, and the digests are functions of the inputs
// alone (REQ-publish-determinism).
package publish

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/plugin/genfile"
)

// Tree is one platform's input: the directory whose files are the
// image's, and optionally the base the image is built over, a
// reference with a digest (plugin-publish.md, the platform tree and
// base terms).
type Tree struct {
	Dir  string
	Base string
}

// Request is one build (REQ-publish-verb).
type Request struct {
	// Reference is the full plugin reference with a tag to publish at.
	Reference string
	// Entrypoint is the plugin process's argv, its first element an
	// absolute path within every platform tree.
	Entrypoint []string
	// Executables names, as absolute paths within the trees, the files
	// the image marks executable beside the entrypoint, which always
	// is: executability is an input, never the host's mode bits
	// (REQ-publish-image).
	Executables []string
	// Platforms maps `<os>/<arch>` to its tree.
	Platforms map[string]Tree
	// Transport and Keychain reach the registries; nil is the
	// registry client's own and the ambient credential store.
	Transport http.RoundTripper
	Keychain  authn.Keychain
}

// Published is what a build reports: the list's digest, each
// platform's image digest in the platforms' spelling order, and
// whether the tag already held this very list, in which case nothing
// was published (REQ-publish-immutable).
type Published struct {
	Reference string
	Digest    string
	Images    []PlatformImage
	Unchanged bool
}

// PlatformImage is one platform's image as published.
type PlatformImage struct {
	Platform string
	Digest   string
}

// ErrTagTaken refuses a reference whose tag names another list.
var ErrTagTaken = errors.New("the tag names another image already")

// ParseRequest reads the verb's arguments as the command line spells
// them (REQ-publish-verb): the reference, the entrypoint's argv, the
// executables, `<os>/<arch>=<directory>` per platform and
// `<os>/<arch>=<reference>@<digest>` per base — a value that does not
// parse, a platform or base given twice, or a base for a platform
// given no tree, refused naming the flag.
func ParseRequest(reference string, entrypoint, executables, platforms, bases []string) (Request, error) {
	req := Request{Reference: reference, Entrypoint: entrypoint, Executables: executables, Platforms: map[string]Tree{}}
	for _, v := range platforms {
		p, dir, ok := strings.Cut(v, "=")
		if !ok || p == "" || dir == "" {
			return Request{}, fmt.Errorf("--platform %q is not <os>/<arch>=<directory>", v)
		}
		if _, dup := req.Platforms[p]; dup {
			return Request{}, fmt.Errorf("--platform %s given twice", p)
		}
		req.Platforms[p] = Tree{Dir: dir}
	}
	for _, v := range bases {
		p, ref, ok := strings.Cut(v, "=")
		if !ok || p == "" || ref == "" {
			return Request{}, fmt.Errorf("--base %q is not <os>/<arch>=<reference>@<digest>", v)
		}
		t, given := req.Platforms[p]
		if !given {
			return Request{}, fmt.Errorf("--base %s names a platform no --platform gives a tree for", p)
		}
		if t.Base != "" {
			return Request{}, fmt.Errorf("--base %s given twice", p)
		}
		t.Base = ref
		req.Platforms[p] = t
	}
	return req, nil
}

// Build validates the request, builds the images and the list, and
// publishes them (REQ-publish-verb).
func Build(ctx context.Context, req Request) (*Published, error) {
	// The reference is spelled as the generation file spells one
	// (generation.md REQ-gen-schema): a full reference with a tag.
	if err := genfile.CheckReference(req.Reference); err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}
	tag, err := name.NewTag(req.Reference, name.StrictValidation)
	if err != nil {
		return nil, fmt.Errorf("publish: reference %q: %w", req.Reference, err)
	}
	opts := []remote.Option{remote.WithContext(ctx)}
	if req.Transport != nil {
		opts = append(opts, remote.WithTransport(req.Transport))
	}
	keychain := req.Keychain
	if keychain == nil {
		keychain = authn.DefaultKeychain
	}
	opts = append(opts, remote.WithAuthFromKeychain(keychain))
	idx, images, err := assemble(req, opts)
	if err != nil {
		return nil, err
	}
	digest, err := idx.Digest()
	if err != nil {
		return nil, err
	}
	out := &Published{Reference: req.Reference, Digest: digest.String(), Images: images}

	// A tag means one image forever (REQ-publish-immutable).
	held, err := remote.Head(tag, opts...)
	switch {
	case err == nil && held.Digest == digest:
		out.Unchanged = true
		return out, nil
	case err == nil:
		return nil, fmt.Errorf("publish: %s holds %s, and this build is %s: %w", req.Reference, held.Digest, digest, ErrTagTaken)
	case !isNotFound(err):
		return nil, fmt.Errorf("publish: reading %s (a registry that will not say whether the tag exists — one refusing a repository not yet created, say — refuses the publish; create the repository first): %w", req.Reference, err)
	}
	if err := remote.WriteIndex(tag, idx, opts...); err != nil {
		return nil, fmt.Errorf("publish: pushing %s: %w", req.Reference, err)
	}
	return out, nil
}

// assemble builds the images and the list in process, publishing
// nothing: what the digests are functions of is all here
// (REQ-publish-determinism). opts reach a base's registry.
func assemble(req Request, opts []remote.Option) (v1.ImageIndex, []PlatformImage, error) {
	if len(req.Entrypoint) == 0 || !strings.HasPrefix(req.Entrypoint[0], "/") {
		return nil, nil, errors.New("publish: --entrypoint's first value is an absolute path within the platform tree")
	}
	executables := map[string]bool{}
	for _, e := range append([]string{req.Entrypoint[0]}, req.Executables...) {
		if !strings.HasPrefix(e, "/") {
			return nil, nil, fmt.Errorf("publish: --executable %q is not an absolute path within the platform tree", e)
		}
		executables[strings.TrimPrefix(path.Clean(e), "/")] = true
	}
	if len(req.Platforms) == 0 {
		return nil, nil, errors.New("publish: no --platform tree given")
	}
	platforms := make([]string, 0, len(req.Platforms))
	for p := range req.Platforms {
		if _, err := plugin.ParsePlatform(p); err != nil {
			return nil, nil, fmt.Errorf("publish: --platform %w", err)
		}
		platforms = append(platforms, p)
	}
	sort.Strings(platforms)
	idx := v1.ImageIndex(empty.Index)
	idx = mutate.IndexMediaType(idx, types.OCIImageIndex)
	var images []PlatformImage
	for _, p := range platforms {
		tree := req.Platforms[p]
		osName, arch, _ := strings.Cut(p, "/")
		pl := v1.Platform{OS: osName, Architecture: arch}
		layer, err := layerOf(tree.Dir, req.Entrypoint[0], executables)
		if err != nil {
			return nil, nil, fmt.Errorf("publish: platform %s: %w", p, err)
		}
		img, err := imageOf(layer, tree.Base, &pl, req.Entrypoint, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("publish: platform %s: %w", p, err)
		}
		d, err := img.Digest()
		if err != nil {
			return nil, nil, err
		}
		images = append(images, PlatformImage{Platform: p, Digest: d.String()})
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &pl}})
	}
	return idx, images, nil
}

// isNotFound reports a registry's not-found for the tag — a 404, or
// the manifest or the repository unknown by the registry's own code
// — the one answer a first publish expects, the reference free to
// publish at.
func isNotFound(err error) bool {
	var te *transport.Error
	if !errors.As(err, &te) {
		return false
	}
	if te.StatusCode == http.StatusNotFound {
		return true
	}
	for _, d := range te.Errors {
		if d.Code == transport.ManifestUnknownErrorCode || d.Code == transport.NameUnknownErrorCode {
			return true
		}
	}
	return false
}

// imageOf assembles one platform's image (REQ-publish-image): from
// scratch, or over the base fetched at its digest and refused unless
// its configuration names the platform, that configuration kept but
// for the entrypoint, cmd, os, architecture, variant, creation time
// and history, the base's OS version carried onto the list's entry.
func imageOf(layer v1.Layer, base string, pl *v1.Platform, entrypoint []string, opts []remote.Option) (v1.Image, error) {
	img := v1.Image(empty.Image)
	cfg := &v1.ConfigFile{}
	if base != "" {
		ref, err := name.NewDigest(base, name.StrictValidation)
		if err != nil {
			return nil, fmt.Errorf("base %q is not a reference with a digest: %w", base, err)
		}
		fetched, err := baseImage(ref, pl, opts)
		if err != nil {
			return nil, fmt.Errorf("base %s: %w", base, err)
		}
		if cfg, err = fetched.ConfigFile(); err != nil {
			return nil, fmt.Errorf("base %s: %w", base, err)
		}
		// A digest naming one image is that image whatever platform
		// was asked for: the base is the platform's only where its
		// configuration says so.
		if cfg.OS != pl.OS || cfg.Architecture != pl.Architecture {
			return nil, fmt.Errorf("base %s is %s/%s, not %s", base, cfg.OS, cfg.Architecture, pl.String())
		}
		// The list's entry names the OS version as the configuration
		// does: the caller's platform carries it onto the descriptor.
		pl.OSVersion = cfg.OSVersion
		img = fetched
	} else {
		cfg.Config.User = "65534:65534"
		cfg.Config.WorkingDir = "/"
	}
	img, err := mutate.AppendLayers(img, layer)
	if err != nil {
		return nil, err
	}
	cfg = cfg.DeepCopy()
	cfg.OS, cfg.Architecture, cfg.Variant = pl.OS, pl.Architecture, ""
	cfg.Config.Entrypoint = slices.Clone(entrypoint)
	cfg.Config.Cmd = nil
	cfg.Created = v1.Time{}
	cfg.History = nil
	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		return nil, err
	}
	return mutate.MediaType(mutate.ConfigMediaType(img, types.OCIConfigJSON), types.OCIManifestSchema1), nil
}

// baseImage fetches a base at its digest: the image the digest names,
// or, where it names an index, the index's one entry for the
// platform — matched on OS and architecture, the variant not
// consulted, as the run matches an image's entries
// (plugin-execution.md REQ-plugin-platform-strict) — two or none
// refused, so the base is the publisher's choice and never a
// library's pick among entries, and the output's digest is
// independent of the build of pb (REQ-publish-determinism).
func baseImage(ref name.Digest, pl *v1.Platform, opts []remote.Option) (v1.Image, error) {
	desc, err := remote.Get(ref, opts...)
	if err != nil {
		return nil, err
	}
	if !desc.MediaType.IsIndex() {
		return desc.Image()
	}
	idx, err := desc.ImageIndex()
	if err != nil {
		return nil, err
	}
	man, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	var matches []v1.Hash
	for _, m := range man.Manifests {
		if m.Platform != nil && m.Platform.OS == pl.OS && m.Platform.Architecture == pl.Architecture {
			matches = append(matches, m.Digest)
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("names an index with %d entries for %s, not one", len(matches), pl.String())
	}
	return idx.Image(matches[0])
}

// layerOf writes the platform tree as one canonical tar, uncompressed
// so its digest is the tar's own under every build of pb: entries in
// sorted name order, owned by 0:0 at the zero time, directories 0755,
// files 0755 where named executable — the entrypoint always — and
// 0644 otherwise, the host's mode bits never consulted; a symbolic
// link within the tree is refused, and the entrypoint's path must
// name a regular file of the tree (REQ-publish-image). The tree's
// own path may be a link: its location is no input.
func layerOf(given, entrypoint string, executables map[string]bool) (v1.Layer, error) {
	dir, err := filepath.EvalSymlinks(given)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", given)
	}
	type entry struct {
		rel  string
		info fs.FileInfo
		abs  string
	}
	var entries []entry
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link: an image pb runs carries none", rel)
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("%s is neither a regular file nor a directory", rel)
		}
		name := filepath.ToSlash(rel)
		if info.IsDir() {
			name += "/"
		}
		entries = append(entries, entry{rel: name, info: info, abs: p})
		return nil
	}); err != nil {
		return nil, err
	}
	// Sorted by the entries' tar names — a directory's with its
	// trailing slash — the order the digest is defined over.
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	want := strings.TrimPrefix(path.Clean(entrypoint), "/")
	found := false
	marked := map[string]bool{}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.rel, Uid: 0, Gid: 0, Format: tar.FormatPAX}
		switch {
		case e.info.IsDir():
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		default:
			h.Typeflag, h.Size, h.Mode = tar.TypeReg, e.info.Size(), 0o644
			if executables[e.rel] {
				h.Mode = 0o755
				marked[e.rel] = true
			}
			if e.rel == want {
				found = true
			}
		}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg {
			f, err := os.Open(e.abs)
			if err != nil {
				return nil, err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("the entrypoint %s names no regular file of the tree", entrypoint)
	}
	// Every executable named is a regular file of every tree: one
	// missing would publish an image that fails only when the plugin
	// execs it, at a tag then held forever.
	for _, e := range sortedNames(executables) {
		if !marked[e] {
			return nil, fmt.Errorf("--executable /%s names no regular file of the tree", e)
		}
	}
	return static.NewLayer(buf.Bytes(), types.OCIUncompressedLayer), nil
}

// sortedNames lists a set's members in order.
func sortedNames(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

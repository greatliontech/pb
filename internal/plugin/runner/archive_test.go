package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/greatliontech/pb/internal/plugin"
)

// archiveOf is an export's archive writer over a tree shaped like an
// image export, for the tests: one plain layer of the tree, a
// configuration declaring the process as the image would, in the
// form asked — the classic store's docker-archive, identity the
// configuration's digest; the containerd store's OCI layout,
// identity the manifest's — as pb's store writes them from a
// verified image.
func archiveOf(rootfs string, process plugin.Process, platform plugin.Platform) func(context.Context, io.Writer, plugin.ArchiveForm) (string, error) {
	return func(ctx context.Context, w io.Writer, form plugin.ArchiveForm) (string, error) {
		var layer bytes.Buffer
		if err := layerTar(&layer, rootfs); err != nil {
			return "", err
		}
		layerHex := hexOf(layer.Bytes())
		cfg := map[string]any{
			"architecture": platform.Arch, "os": platform.OS,
			"config": map[string]any{"Entrypoint": process.Argv, "Env": process.Env, "WorkingDir": process.WorkDir},
			"rootfs": map[string]any{"type": "layers", "diff_ids": []string{"sha256:" + layerHex}},
		}
		config, err := json.Marshal(cfg)
		if err != nil {
			return "", err
		}
		configHex := hexOf(config)
		tw := tar.NewWriter(w)
		entry := func(name string, b []byte) error {
			if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(b)), Format: tar.FormatPAX}); err != nil {
				return err
			}
			_, err := tw.Write(b)
			return err
		}
		var id string
		switch form {
		case plugin.DockerArchive:
			manifest, _ := json.Marshal([]map[string]any{{"Config": configHex + ".json", "RepoTags": nil, "Layers": []string{layerHex + "/layer.tar"}}})
			for _, e := range []struct {
				name string
				b    []byte
			}{{"manifest.json", manifest}, {configHex + ".json", config}, {layerHex + "/layer.tar", layer.Bytes()}} {
				if err := entry(e.name, e.b); err != nil {
					return "", err
				}
			}
			id = "sha256:" + configHex
		case plugin.OCILayout:
			manifest, _ := json.Marshal(map[string]any{
				"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
				"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:" + configHex, "size": len(config)},
				"layers": []map[string]any{{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": "sha256:" + layerHex, "size": layer.Len()}},
			})
			manifestHex := hexOf(manifest)
			index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []map[string]any{{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:" + manifestHex, "size": len(manifest)}}})
			for _, e := range []struct {
				name string
				b    []byte
			}{{"oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`)}, {"index.json", index}, {"blobs/sha256/" + manifestHex, manifest}, {"blobs/sha256/" + configHex, config}, {"blobs/sha256/" + layerHex, layer.Bytes()}} {
				if err := entry(e.name, e.b); err != nil {
					return "", err
				}
			}
			id = "sha256:" + manifestHex
		default:
			return "", fmt.Errorf("no archive form %d", form)
		}
		return id, tw.Close()
	}
}

func hexOf(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// layerTar writes the tree as one plain layer: directories, regular
// files and symlinks in lexical order, root-owned, at the epoch, the
// modes declared by place rather than read from the host — an
// executable under bin/ or named plugin 0755, every other file 0644,
// a directory 0755 — since a filesystem without modes (windows)
// records none.
func layerTar(w io.Writer, dir string) error {
	var paths []string
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != dir {
			paths = append(paths, p)
		}
		return nil
	}); err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	for _, p := range paths {
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: name, Mode: 0o644, Format: tar.FormatPAX}
		if strings.HasPrefix(name, "bin/") || name == "plugin" {
			hdr.Mode = 0o755
		}
		switch {
		case fi.IsDir():
			hdr.Typeflag, hdr.Name, hdr.Mode = tar.TypeDir, name+"/", 0o755
		case fi.Mode().IsRegular():
			hdr.Typeflag, hdr.Size = tar.TypeReg, fi.Size()
		case fi.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			hdr.Typeflag, hdr.Linkname, hdr.Mode = tar.TypeSymlink, filepath.ToSlash(target), 0o777
		default:
			return fmt.Errorf("%s: neither a directory, a file nor a symlink", name)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return err
			}
		}
	}
	return tw.Close()
}

// exportOf is an export of the tree with its archive, as the
// acquirer hands one to a runner.
func exportOf(rootfs string, process plugin.Process, platform plugin.Platform) *plugin.Export {
	return &plugin.Export{Rootfs: rootfs, Archive: archiveOf(rootfs, process, platform)}
}

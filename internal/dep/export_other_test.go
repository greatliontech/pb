//go:build !unix

package dep

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestExportIdentityNeverReadsTheHostForAnInMemoryTree pins the
// same-directory identity's gate off unix: an in-memory tree's
// infos carry no host stat, so two of its directories spelled as
// two names of one host directory (darwin's `tmp` and
// `private/tmp`; on windows the test's own directory below its
// volume under two cases, which the filesystem folds) are never
// read as one, and an export beside the module lands where it was
// asked, not refused as inside the module. The host is first held
// to the premise: the two names stat as one file there.
func TestExportIdentityNeverReadsTheHostForAnInMemoryTree(t *testing.T) {
	module, beside := "private/tmp", "tmp"
	if runtime.GOOS == "windows" {
		// The test's own directory, on the current drive by
		// definition, under two cases the filesystem folds.
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		rel := filepath.ToSlash(strings.TrimPrefix(cwd, filepath.VolumeName(cwd)))[1:]
		module, beside = strings.ToUpper(rel), strings.ToLower(rel)
	}
	// The premise, on the host: the two names are one directory.
	host := func(name string) os.FileInfo {
		fi, err := os.Stat(filepath.Join(string(filepath.Separator), filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("the host's %s: %v", name, err)
		}
		return fi
	}
	if !os.SameFile(host(module), host(beside)) {
		t.Fatalf("the host's %s and %s are not one directory: the premise fails here", module, beside)
	}
	fx := newDep(t, map[string]string{
		"pb.work":           "use:\n  - " + module + "\n",
		module + "/pb.yaml": ws("example.com/s", ""),
		module + "/s.proto": "syntax = \"proto3\";\npackage s;\n",
		beside + "/.keep":   "",
	})
	if err := Export(context.Background(), fx.session(t, "."), beside+"/out", beside+"/out", ExportOptions{}, io.Discard); err != nil {
		t.Fatalf("an export beside a module spelled as the host spells the module's host namesake: %v", err)
	}
	if got := tree(t, fx.ws, beside+"/out"); len(got) == 0 {
		t.Fatal("the export landed nowhere")
	}
}

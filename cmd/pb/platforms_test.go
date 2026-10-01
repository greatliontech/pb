package main

import (
	"os/exec"
	"strings"
	"testing"
)

// pb builds for every platform with no C toolchain (platforms.md
// REQ-plat-scope): with cgo disabled, every package the command
// depends on still has Go files to build for each platform — a
// dependency with no pure-Go arm is a package go list reports in
// error, "build constraints exclude all Go files" — and none of them
// carries cgo files into the build. The release workflow's six
// cross-builds are the same claim at the linker.
func TestNoCgo(t *testing.T) {
	// pb's own packages carry no cgo file, cgo enabled or not.
	own := exec.Command("go", "list", "-f", "{{.ImportPath}}|{{.CgoFiles}}", "./...")
	own.Dir = "../.."
	own.Env = append(own.Environ(), "CGO_ENABLED=1")
	out, err := own.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if path, cgo, _ := strings.Cut(line, "|"); cgo != "[]" {
			t.Errorf("%s carries cgo files %s", path, cgo)
		}
	}
	for _, platform := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64"} {
		goos, goarch, _ := strings.Cut(platform, "/")
		cmd := exec.Command("go", "list", "-deps", "-e", "-f", "{{.ImportPath}}|{{.CgoFiles}}|{{if .Error}}{{.Error.Err}}{{end}}", "./cmd/pb")
		cmd.Dir = "../.."
		cmd.Env = append(cmd.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: go list: %v", platform, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			path, rest, _ := strings.Cut(line, "|")
			cgo, errText, _ := strings.Cut(rest, "|")
			if cgo != "[]" {
				t.Errorf("%s: %s carries cgo files %s", platform, path, cgo)
			}
			if errText != "" {
				t.Errorf("%s: %s: %s", platform, path, errText)
			}
		}
	}
}

package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/pb/internal/testing/rapidtest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

var (
	rootfsDir string // the fake plugin built statically for Linux into a bare tree
	rootfsErr error
)

// TestMain builds the fake plugin once into a bare rootfs every runner
// consumes, pins the property oracles' seed (rapidtest), hands the
// platform's sandbox probe the run, and — under the fake-daemon
// marker — is the docker command the contract tests put on PATH.
func TestMain(m *testing.M) {
	if os.Getenv(fakeDockerEnv) != "" {
		os.Exit(fakeDocker(os.Args[1:]))
	}
	dir, err := os.MkdirTemp("", "pb-rootfs-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "plugin"), "testdata/fakeplugin.go")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if out, err := cmd.CombinedOutput(); err != nil {
		rootfsErr = fmt.Errorf("building the fake plugin: %v\n%s", err, out)
	} else {
		rootfsDir = dir
	}
	rapidtest.Pin()
	code := setupSandbox(m)
	os.RemoveAll(dir)
	os.Exit(code)
}

func request(t *testing.T, param string) []byte {
	t.Helper()
	b, err := proto.Marshal(&pluginpb.CodeGeneratorRequest{FileToGenerate: []string{"a.proto", "b.proto"}, Parameter: &param})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func limits(mod func(*trust.Limits)) trust.Limits {
	l := (&trust.Execution{}).EffectiveLimits()
	if mod != nil {
		mod(&l)
	}
	return l
}

func content(t *testing.T, res *Result) string {
	t.Helper()
	resp, err := Respond(res)
	if err != nil {
		t.Fatal(err)
	}
	return resp.GetFile()[0].GetContent()
}

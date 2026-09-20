package testing_test

import (
	"os/exec"
	"strings"
	"testing"
)

// Nothing under internal/testing ships: no package outside it imports
// one of its packages other than from a test file. The fixtures import
// one another freely.
func TestFixturesAreTestOnly(t *testing.T) {
	const module, home = "github.com/greatliontech/pb", "github.com/greatliontech/pb/internal/testing/"
	// Without -test, go list names each package's non-test imports.
	// A pattern matching nothing is a warning and an empty listing, not
	// an error, so the listing is held to name the fixtures themselves.
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", module+"/...")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	fixtures := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if strings.HasPrefix(fields[0], home) {
			fixtures++
			continue
		}
		for _, imp := range fields[1:] {
			if strings.HasPrefix(imp, home) {
				t.Errorf("%s ships %s", fields[0], imp)
			}
		}
	}
	if fixtures == 0 {
		t.Fatalf("the listing names no fixture package:\n%s", out)
	}
}

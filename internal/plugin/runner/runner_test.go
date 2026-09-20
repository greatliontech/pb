package runner

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/greatliontech/pb/internal/provenance/trust"
)

// A Spec with any unbounded resource is refused before anything runs
// (REQ-plugin-resource-bounds): the defaults are the trust policy's
// to fold, and a zero field reaching the runner is a caller error.
func TestCheckLimits(t *testing.T) {
	full := (&trust.Execution{}).EffectiveLimits()
	if err := checkLimits(full); err != nil {
		t.Fatalf("effective limits refused: %v", err)
	}
	for name, mod := range map[string]func(*trust.Limits){
		"memory":  func(l *trust.Limits) { l.Memory = 0 },
		"cpu":     func(l *trust.Limits) { l.CPU = 0 },
		"pids":    func(l *trust.Limits) { l.Pids = 0 },
		"timeout": func(l *trust.Limits) { l.Timeout = 0 },
	} {
		l := full
		mod(&l)
		if err := checkLimits(l); err == nil || !strings.Contains(err.Error(), "unbounded") {
			t.Errorf("%s zero accepted: %v", name, err)
		}
	}
	if err := checkLimits(trust.Limits{Memory: 1, CPU: 1, Pids: 1, Timeout: time.Second}); err != nil {
		t.Fatalf("minimal limits refused: %v", err)
	}
}

// tailBytes keeps short output whole (512 exactly included) and the
// last 512 bytes of anything longer.
func TestTailBytes(t *testing.T) {
	at := bytes.Repeat([]byte("a"), 512)
	if got := tailBytes(at); !bytes.Equal(got, at) {
		t.Fatalf("512 bytes truncated to %d", len(got))
	}
	long := append([]byte("x"), at...)
	if got := tailBytes(long); !bytes.Equal(got, at) {
		t.Fatalf("tail of 513 = %d bytes, first %q", len(got), got[:1])
	}
}

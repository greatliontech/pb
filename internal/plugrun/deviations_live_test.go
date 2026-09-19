package plugrun

import (
	"context"
	"maps"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/greatliontech/pb/internal/plugexec"
	"github.com/greatliontech/pb/internal/plugrun/testdata/behavior"
)

// mount is one entry of the fixture's mount table: the filesystem
// type, the device naming the filesystem instance, and the mounted
// subtree of it — "/" for a mount the runtime created, a subpath for
// a bind.
type mount struct{ typ, device, root string }

// worldReport is the fixture's world probe parsed: the mount table
// by mount point ("unreadable" alone where /proc is not mounted),
// environment names, which of the probe's paths exist, and which of
// its writes succeeded.
type worldReport struct {
	unreadable bool
	mounts     map[string]mount
	env        []string
	present    []string
	writes     map[string]bool
}

func world(t *testing.T, r Runner) worldReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res, err := r.Run(ctx, Spec{Scheme: plugexec.SchemeOCI, Rootfs: rootfsDir, Process: plugexec.Process{Argv: []string{"/plugin"}, Env: []string{"PB_PLUGIN_TEST_ENV=from-the-image"}}, Stdin: request(t, behavior.World), Limits: limits(nil), MinTier: plugexec.TierStrong})
	if err != nil {
		t.Fatal(err)
	}
	w := worldReport{mounts: map[string]mount{}, writes: map[string]bool{}}
	for _, part := range strings.Fields(content(t, res)) {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "mounts":
			if v == "unreadable" {
				w.unreadable = true
				continue
			}
			for _, m := range strings.Split(v, ",") {
				f := strings.SplitN(m, ":", 4)
				if len(f) != 4 {
					t.Fatalf("mount entry %q", m)
				}
				w.mounts[behavior.Unescape(f[3])] = mount{typ: f[0], device: behavior.Unescape(f[1]), root: behavior.Unescape(f[2])}
			}
		case "env":
			w.env = strings.Split(v, ",")
		case "present":
			if v != "" {
				w.present = strings.Split(v, ",")
			}
		case "writes":
			for _, m := range strings.Split(v, ",") {
				p, ok, _ := strings.Cut(m, ":")
				w.writes[p] = ok == "true"
			}
		}
	}
	return w
}

// The variables the daemon injects (REQ-plugin-sandboxed); the
// runtime roots and name files are the probe's own (behavior).
var injectedVars = []string{"PATH", "HOSTNAME", "HOME"}

func underRuntimeRoot(point string) bool {
	for _, root := range behavior.RuntimeRoots {
		if point == root || strings.HasPrefix(point, root+"/") {
			return true
		}
	}
	return false
}

// The docker runner's world beyond the image is exactly the named
// deviations: every mount over the image's root is a filesystem the
// runtime created under one of the three roots — mounted whole, or
// re-bound from one of those very filesystems (the runtime's masks),
// never a host directory bound there — or one of the daemon's name
// files; every variable beyond the image's is one the
// daemon injects; /dev and the runtime's masks are writable, /proc
// itself and the root are not. The native runner's world is the
// image's alone: no /proc to read mounts from, none of the runtime
// roots or name files, no write anywhere (REQ-plugin-sandboxed's
// deviation list, INV-docker-deviations).
func TestDockerDeviations(t *testing.T) {
	docker := requireDaemon(t)
	w := world(t, docker)
	if w.unreadable || len(w.mounts) == 0 {
		t.Fatal("the container's mount table was not read")
	}
	// The runtime's own filesystem instances: what it mounted whole
	// at the three roots. A bind under them from any other instance
	// is a host directory's.
	runtimeDevices := map[string]bool{}
	for _, root := range behavior.RuntimeRoots {
		m, ok := w.mounts[root]
		if !ok || m.root != "/" {
			t.Fatalf("%s is not a filesystem the runtime mounted whole: %+v", root, m)
		}
		runtimeDevices[m.device] = true
	}
	for point, m := range w.mounts {
		switch {
		case point == "/":
		case underRuntimeRoot(point):
			if m.root != "/" && !runtimeDevices[m.device] {
				t.Errorf("%s is a bind of a host directory (%s %s at %s) under a runtime root, not a filesystem the runtime created", point, m.typ, m.device, m.root)
			}
		case slices.Contains(behavior.NameFiles, point):
		default:
			t.Errorf("the daemon mounted %s (%s) over the image's root: not a named deviation", point, m.typ)
		}
	}
	for _, v := range w.env {
		if v != "PB_PLUGIN_TEST_ENV" && !slices.Contains(injectedVars, v) {
			t.Errorf("the daemon injected %s: not a named deviation", v)
		}
	}
	if want := slices.Sorted(slices.Values(behavior.Present)); !slices.Equal(w.present, want) {
		t.Errorf("present under the daemon = %v, want %v", w.present, want)
	}
	if want := map[string]bool{"/dev/probe": true, "/proc/interrupts": true, "/proc/probe": false, "/probe": false}; !maps.Equal(w.writes, want) {
		t.Errorf("writes under the daemon = %v, want %v (/dev and the runtime's masks writable, /proc itself and the root not)", w.writes, want)
	}
	if runtime.GOOS != "linux" {
		return
	}
	native, err := NativeRunner()
	if err != nil {
		t.Fatal(err)
	}
	requireSandbox(t)
	w = world(t, native)
	if !w.unreadable || len(w.present) != 0 || !slices.Equal(w.env, []string{"PB_PLUGIN_TEST_ENV"}) {
		t.Errorf("the native runner's world beyond the image: mounts readable %v, present %v, env %v", !w.unreadable, w.present, w.env)
	}
	for p, ok := range w.writes {
		if ok {
			t.Errorf("the native runner let the plugin write %s", p)
		}
	}
}

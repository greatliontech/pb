package plugrun

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

// The fake daemon: the test binary re-invoked as `docker` from a PATH
// the contract tests set, keeping its state in a directory the test
// owns. Every invocation is logged there; the record `inspect`
// returns is built from the `create` arguments actually passed, so
// the derivation reads what the runner asked for, patched by what the
// test plants (record.json for the host config, state.json for the
// state after the run); `import` keeps the tar it was fed; `start`
// answers with the planted response or hangs until `kill`.
const (
	fakeDockerEnv = "PB_FAKE_DOCKER"
	fakeDockerDir = "PB_FAKE_DOCKER_DIR"
)

func fakeDocker(args []string) int {
	dir := os.Getenv(fakeDockerDir)
	logf, _ := os.OpenFile(filepath.Join(dir, "log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	line, _ := json.Marshal(args)
	fmt.Fprintf(logf, "%s\n", line)
	logf.Close()
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "version":
		if _, err := os.Stat(filepath.Join(dir, "unavailable")); err == nil {
			fmt.Fprintln(os.Stderr, "Cannot connect to the Docker daemon at unix:///var/run/docker.sock")
			return 1
		}
		fmt.Println("linux fakearch")
	case "info":
		if b, err := os.ReadFile(filepath.Join(dir, "info.json")); err == nil {
			os.Stdout.Write(b)
		} else {
			fmt.Println(`["name=seccomp,profile=builtin","name=cgroupns"]`)
		}
	case "import":
		tar, _ := io.ReadAll(os.Stdin)
		os.WriteFile(filepath.Join(dir, "import.tar"), tar, 0o644)
		fmt.Printf("sha256:%x\n", sha256.Sum256(tar))
	case "create":
		b, _ := json.Marshal(args[1:])
		os.WriteFile(filepath.Join(dir, "create.json"), b, 0o644)
		fmt.Println("fakecontainer")
	case "inspect":
		rec := fakeRecord(dir)
		b, _ := json.Marshal([]any{rec})
		os.Stdout.Write(b)
	case "start":
		stdin, _ := io.ReadAll(os.Stdin)
		os.WriteFile(filepath.Join(dir, "stdin"), stdin, 0o644)
		mode, _ := os.ReadFile(filepath.Join(dir, "start"))
		if string(mode) == "hang" {
			for i := 0; i < 6000; i++ {
				if _, err := os.Stat(filepath.Join(dir, "killed")); err == nil {
					os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"Status":"exited","ExitCode":137}`), 0o644)
					return 137
				}
				time.Sleep(10 * time.Millisecond)
			}
			return 3
		}
		if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
			os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"Status":"exited","ExitCode":0}`), 0o644)
		}
		out, err := os.ReadFile(filepath.Join(dir, "stdout"))
		if err != nil {
			out, _ = proto.Marshal(&pluginpb.CodeGeneratorResponse{File: []*pluginpb.CodeGeneratorResponse_File{{Name: proto.String("out.txt"), Content: proto.String("from the fake daemon")}}})
		}
		os.Stdout.Write(out)
		if errOut, err := os.ReadFile(filepath.Join(dir, "stderr")); err == nil {
			os.Stderr.Write(errOut)
		}
	case "kill":
		os.WriteFile(filepath.Join(dir, "killed"), nil, 0o644)
	case "rm", "rmi":
	default:
		fmt.Fprintf(os.Stderr, "fake docker: unknown command %q\n", args[0])
		return 2
	}
	return 0
}

// fakeRecord builds the daemon's record from the create arguments,
// then applies the planted patches.
func fakeRecord(dir string) map[string]any {
	var args []string
	if b, err := os.ReadFile(filepath.Join(dir, "create.json")); err == nil {
		json.Unmarshal(b, &args)
	}
	hc := map[string]any{"NetworkMode": "bridge", "ReadonlyRootfs": false, "Privileged": false, "Isolation": "", "CapDrop": []string{}, "SecurityOpt": []string{}, "Memory": 0, "MemorySwap": 0, "PidsLimit": nil, "Ulimits": []any{}}
	cfg := map[string]any{"Hostname": "0123456789ab", "Env": []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}}
	caps, secs, ulimits, env := []string{}, []string{}, []any{}, []string{}
	for i := 0; i < len(args); i++ {
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch args[i] {
		case "--network":
			hc["NetworkMode"] = next()
		case "--read-only":
			hc["ReadonlyRootfs"] = true
		case "--memory":
			hc["Memory"], _ = strconv.ParseInt(next(), 10, 64)
		case "--memory-swap":
			hc["MemorySwap"], _ = strconv.ParseInt(next(), 10, 64)
		case "--hostname":
			cfg["Hostname"] = next()
		case "--env":
			env = append(env, next())
		case "--pids-limit":
			n, _ := strconv.ParseInt(next(), 10, 64)
			hc["PidsLimit"] = n
		case "--ulimit":
			name, val, _ := strings.Cut(next(), "=")
			n, _ := strconv.ParseInt(val, 10, 64)
			ulimits = append(ulimits, map[string]any{"Name": name, "Hard": n, "Soft": n})
		case "--cap-drop":
			caps = append(caps, next())
		case "--security-opt":
			secs = append(secs, next())
		case "--entrypoint", "--workdir":
			next()
		}
	}
	hc["CapDrop"], hc["SecurityOpt"], hc["Ulimits"] = caps, secs, ulimits
	cfg["Env"] = append(env, cfg["Env"].([]string)...)
	rec := map[string]any{"Config": cfg, "HostConfig": hc, "AppArmorProfile": ""}
	if b, err := os.ReadFile(filepath.Join(dir, "record.json")); err == nil {
		// A patch names HostConfig fields, or a top-level section
		// under "Config" / "AppArmorProfile".
		var patch map[string]any
		json.Unmarshal(b, &patch)
		for k, v := range patch {
			switch k {
			case "Config":
				for ck, cv := range v.(map[string]any) {
					cfg[ck] = cv
				}
			case "AppArmorProfile":
				rec[k] = v
			default:
				hc[k] = v
			}
		}
	}
	state := map[string]any{"Status": "created", "ExitCode": 0, "OOMKilled": false}
	if b, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
		json.Unmarshal(b, &state)
	}
	rec["State"] = state
	return rec
}

// fakeDaemon puts the fake docker on PATH for one test and returns
// its state directory.
func fakeDaemon(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, "docker")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("PATH", bin)
	t.Setenv(fakeDockerEnv, "1")
	t.Setenv(fakeDockerDir, dir)
	return dir
}

// fakeLog returns the fake daemon's invocations, first argument each.
func fakeLog(t *testing.T, dir string) (verbs []string, argv [][]string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		verbs = append(verbs, args[0])
		argv = append(argv, args)
	}
	return verbs, argv
}

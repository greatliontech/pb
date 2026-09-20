//go:build ignore

// fakeplugin is the runner suite's plugin process: it reads a
// CodeGeneratorRequest whose parameter selects a behavior.
package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/greatliontech/pb/internal/plugin/runner/testdata/behavior"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

func main() {
	in, _ := io.ReadAll(os.Stdin)
	var req pluginpb.CodeGeneratorRequest
	if err := proto.Unmarshal(in, &req); err != nil {
		fmt.Fprintln(os.Stderr, "bad request:", err)
		os.Exit(2)
	}
	switch req.GetParameter() {
	case behavior.Exit7:
		fmt.Fprintln(os.Stderr, "deliberate failure")
		os.Exit(7)
	case behavior.Sleep:
		time.Sleep(5 * time.Minute)
	case behavior.Spin:
		// Burn CPU on every core: CPU time outruns the wall clock, so
		// a CPU-time bound fires before a wall-clock one.
		for i := 0; i < runtime.NumCPU(); i++ {
			go func() {
				for {
				}
			}()
		}
		time.Sleep(5 * time.Minute)
	case behavior.Net:
		// The interface inventory, then a dial: an isolated network
		// namespace has exactly a loopback, and the dial fails
		// whether or not the host is online.
		var names []string
		ifaces, _ := net.Interfaces()
		for _, i := range ifaces {
			names = append(names, i.Name)
		}
		sort.Strings(names)
		_, err := net.DialTimeout("tcp", "1.1.1.1:443", 2*time.Second)
		emit(fmt.Sprintf("ifaces=%s dial-err=%v", strings.Join(names, ","), err != nil))
		return
	case behavior.Env:
		emit("PB_PLUGIN_TEST_ENV=" + os.Getenv("PB_PLUGIN_TEST_ENV"))
		return
	case behavior.Host:
		h, _ := os.Hostname()
		emit("hostname=" + h)
		return
	case behavior.Both:
		fmt.Fprintln(os.Stderr, "a line on stderr")
		emit("stdout-with-stderr")
		return
	case behavior.Write:
		err := os.WriteFile("/probe", []byte("x"), 0o644)
		emit(fmt.Sprintf("write-err=%v", err != nil))
		return
	case behavior.World:
		// The world as the process sees it: every mount as
		// type:device:root:point ("unreadable" where /proc is not
		// mounted; the device names the filesystem instance, the root
		// the mounted subtree of it — "/" for a mount the runtime
		// created, a subpath for a bind), every environment
		// variable's name, which of the probe's paths exist, and
		// which of its writes succeed.
		mounts := "unreadable"
		if mi, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
			var entries []string
			for _, line := range strings.Split(strings.TrimSpace(string(mi)), "\n") {
				f := strings.Fields(line)
				// The separator is the first "-" from the seventh
				// field on: the six fields before it are fixed, and a
				// mount point spelled "-" sits among them.
				sep := -1
				for i := 6; i < len(f); i++ {
					if f[i] == "-" {
						sep = i
						break
					}
				}
				if len(f) > 4 && sep > 0 && sep+1 < len(f) {
					entries = append(entries, f[sep+1]+":"+behavior.Escape(f[2])+":"+behavior.Escape(f[3])+":"+behavior.Escape(f[4]))
				}
			}
			sort.Strings(entries)
			mounts = strings.Join(entries, ",")
		}
		var names []string
		for _, kv := range os.Environ() {
			names = append(names, strings.SplitN(kv, "=", 2)[0])
		}
		sort.Strings(names)
		var present []string
		for _, p := range behavior.Present {
			if _, err := os.Stat(p); err == nil {
				present = append(present, p)
			}
		}
		sort.Strings(present)
		var writes []string
		for _, p := range behavior.Writes {
			err := os.WriteFile(p, []byte("x"), 0o644)
			writes = append(writes, fmt.Sprintf("%s:%v", p, err == nil))
		}
		emit(fmt.Sprintf("mounts=%s env=%s present=%s writes=%s", mounts, strings.Join(names, ","), strings.Join(present, ","), strings.Join(writes, ",")))
		return
	case behavior.Hog:
		var chunks [][]byte
		for i := 0; i < 1<<14; i++ {
			chunks = append(chunks, make([]byte, 1<<20))
			for j := range chunks[i] {
				chunks[i][j] = byte(j)
			}
		}
		emit(fmt.Sprintf("hogged=%d", len(chunks)))
		return
	}
	emit("files=" + fmt.Sprint(len(req.GetFileToGenerate())))
}

func emit(msg string) {
	out, _ := proto.Marshal(&pluginpb.CodeGeneratorResponse{File: []*pluginpb.CodeGeneratorResponse_File{{Name: proto.String("out.txt"), Content: proto.String(msg)}}})
	os.Stdout.Write(out)
}

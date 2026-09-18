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
	case "exit7":
		fmt.Fprintln(os.Stderr, "deliberate failure")
		os.Exit(7)
	case "sleep":
		time.Sleep(5 * time.Minute)
	case "spin":
		// Burn CPU on every core: CPU time outruns the wall clock, so
		// a CPU-time bound fires before a wall-clock one.
		for i := 0; i < runtime.NumCPU(); i++ {
			go func() {
				for {
				}
			}()
		}
		time.Sleep(5 * time.Minute)
	case "net":
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
	case "write":
		err := os.WriteFile("/probe", []byte("x"), 0o644)
		emit(fmt.Sprintf("write-err=%v", err != nil))
		return
	case "hog":
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

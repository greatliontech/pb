package runner

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

// Respond's authority table (REQ-plugin-response-authority): output
// comes exclusively from a well-formed, non-erroring response.
func TestRespond(t *testing.T) {
	ok, _ := proto.Marshal(&pluginpb.CodeGeneratorResponse{File: []*pluginpb.CodeGeneratorResponse_File{{Name: proto.String("a.go"), Content: proto.String("x")}}})
	resp, err := Respond(&Result{Stdout: ok})
	if err != nil || len(resp.GetFile()) != 1 {
		t.Fatalf("ok path: %v %v", resp, err)
	}

	if _, err := Respond(&Result{ExitCode: 3, Stderr: []byte("boom")}); err == nil || !strings.Contains(err.Error(), "exited 3") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("exit: %v", err)
	}
	if _, err := Respond(&Result{Stdout: []byte("\xff\xff not proto"), Stderr: []byte("ctx")}); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("malformed: %v", err)
	}
	declared, _ := proto.Marshal(&pluginpb.CodeGeneratorResponse{Error: proto.String("bad option value")})
	if _, err := Respond(&Result{Stdout: declared}); err == nil || !strings.Contains(err.Error(), "bad option value") {
		t.Fatalf("declared error: %v", err)
	}
	// The declared error surfaces verbatim even alongside files.
	both, _ := proto.Marshal(&pluginpb.CodeGeneratorResponse{Error: proto.String("half done"), File: []*pluginpb.CodeGeneratorResponse_File{{Name: proto.String("a")}}})
	if _, err := Respond(&Result{Stdout: both}); err == nil || !strings.Contains(err.Error(), "half done") {
		t.Fatalf("error beats files: %v", err)
	}
}

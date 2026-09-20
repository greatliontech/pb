package runner

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

// Respond judges a run's output (REQ-plugin-response-authority):
// generated output comes exclusively from the CodeGeneratorResponse —
// a non-zero exit, a malformed response, or a declared response error
// fails with the plugin's own words surfaced verbatim.
func Respond(res *Result) (*pluginpb.CodeGeneratorResponse, error) {
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("plugin exited %d: %s", res.ExitCode, tailBytes(res.Stderr))
	}
	var resp pluginpb.CodeGeneratorResponse
	if err := proto.Unmarshal(res.Stdout, &resp); err != nil {
		return nil, fmt.Errorf("plugin wrote a malformed response: %v (stderr: %s)", err, tailBytes(res.Stderr))
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("plugin: %s", resp.GetError())
	}
	return &resp, nil
}

func tailBytes(b []byte) []byte {
	const n = 512
	if len(b) <= n {
		return b
	}
	return b[len(b)-n:]
}

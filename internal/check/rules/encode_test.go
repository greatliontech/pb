package rules

import (
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/check"
)

// Encode renders a rule file canonically and round-trips through
// Parse; what Parse refuses, Encode refuses (REQ-rules-emission).
func TestEncode(t *testing.T) {
	f := &File{
		CELEnv: 1,
		Imports: []Import{
			{Path: "github.com/greatliontech/buf-rules", Version: "v0.2.0", Alias: "buf"},
			{Path: "example.com/acme/mono/proto", Alias: "local"},
		},
		Functions: []Function{
			{Name: "endsWithSuffix", Params: []Param{{Name: "name", Type: Type{Name: "string"}}, {Name: "suffix", Type: Type{Name: "string"}}}, Returns: Type{Name: "bool"}, CEL: "name.endsWith(suffix)"},
			{Name: "always", Returns: Type{Name: "bool"}, CEL: "true"},
			{Name: "names", Params: []Param{{Name: "xs", Type: Type{Name: "list", Args: []Type{{Name: "string"}}}}}, Returns: Type{Name: "map", Args: []Type{{Name: "string"}, {Name: "bool"}}}, CEL: "xs.map(x, x == 'a')"},
		},
		Rules: []Rule{
			{ID: "SERVICE_SUFFIX_Svc", Kind: check.KindLint, Target: check.TargetService, Severity: check.SeverityError, CEL: "buf.serviceSuffix(service, 'Svc')", Message: "service names end in Svc"},
			{ID: "ODD", Kind: check.KindBreaking, Target: check.TargetFile, Severity: check.SeverityWarning, Tags: []string{"FILE", "true"}, CEL: "true\n|| false\n", Message: "a: b"},
		},
	}
	out, err := Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	want := `celEnv: 1
imports:
  - path: github.com/greatliontech/buf-rules
    version: v0.2.0
    alias: buf
  - path: example.com/acme/mono/proto
    alias: local
functions:
  - name: endsWithSuffix
    params:
      - name: name
        type: string
      - name: suffix
        type: string
    returns: bool
    cel: name.endsWith(suffix)
  - name: always
    params: []
    returns: bool
    cel: "true"
  - name: names
    params:
      - name: xs
        type: list(string)
    returns: map(string, bool)
    cel: xs.map(x, x == 'a')
rules:
  - id: SERVICE_SUFFIX_Svc
    kind: lint
    target: service
    severity: error
    cel: buf.serviceSuffix(service, 'Svc')
    message: service names end in Svc
  - id: ODD
    kind: breaking
    target: file
    severity: warning
    tags:
      - FILE
      - "true"
    cel: "true\n|| false\n"
    message: "a: b"
`
	if string(out) != want {
		t.Fatalf("Encode:\n%s", out)
	}
	again, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if twice, err := Encode(again); err != nil || string(twice) != want {
		t.Fatalf("round trip: %v\n%s", err, twice)
	}
	if out, err := Encode(&File{CELEnv: 1}); err != nil || string(out) != "celEnv: 1\nrules: []\n" {
		t.Fatalf("no rules: %q %v", out, err)
	}
	for name, f := range map[string]*File{
		"nil":            nil,
		"env unprovided": {CELEnv: 99},
		"id with colon":  {CELEnv: 1, Rules: []Rule{{ID: "a:b", Kind: check.KindLint, Target: check.TargetFile, Severity: check.SeverityError, CEL: "true", Message: "m"}}},
		"no message":     {CELEnv: 1, Rules: []Rule{{ID: "A", Kind: check.KindLint, Target: check.TargetFile, Severity: check.SeverityError, CEL: "true"}}},
		"bad alias":      {CELEnv: 1, Imports: []Import{{Path: "example.com/x", Version: "v1.0.0", Alias: "1x"}}},
		"function name":  {CELEnv: 1, Functions: []Function{{Name: "1f", Returns: Type{Name: "bool"}, CEL: "true"}}},
	} {
		if _, err := Encode(f); err == nil {
			t.Errorf("%s: encoded", name)
		} else if !strings.Contains(err.Error(), "invalid rule file") && !strings.Contains(err.Error(), "unprovided CEL environment") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

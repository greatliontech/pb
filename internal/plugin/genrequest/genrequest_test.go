package genrequest

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile/linker"
	"github.com/greatliontech/pb/internal/plugin/genfile"
	"github.com/greatliontech/pb/internal/proto/compile"
	"github.com/greatliontech/pb/internal/proto/modfiles"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
	"pgregory.net/rapid"
)

var ctx = context.Background()

func compileMods(t testing.TB, mods []modfiles.Module) *compile.Result {
	t.Helper()
	res, err := compile.Compile(ctx, mods)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// The generation targets are the workspace files the entry's patterns select,
// in compile order, any pattern matching; every workspace file with
// none; a pattern selecting nothing fails naming it, whatever the
// others select; with imports
// included, what the selected reach through imports follows in
// topological order, the well-known imports and the selected left
// out, each once (REQ-gen-request, the generation target term).
func TestTargets(t *testing.T) {
	res := fixture(t)
	all := func(entry genfile.Plugin) []string {
		t.Helper()
		got, err := Targets(res.Topological(), res.Files, entry, modfiles.WellKnown)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := all(genfile.Plugin{}); !slices.Equal(got, []string{"a/a.proto", "a/b.proto"}) {
		t.Errorf("no pattern: %v", got)
	}
	if got := all(genfile.Plugin{Files: []string{"a/b.proto"}}); !slices.Equal(got, []string{"a/b.proto"}) {
		t.Errorf("one file: %v", got)
	}
	if got := all(genfile.Plugin{Files: []string{"a/b.proto", "a/*.proto"}}); !slices.Equal(got, []string{"a/a.proto", "a/b.proto"}) {
		t.Errorf("any pattern, each file once: %v", got)
	}
	// A dead pattern fails whatever the others select.
	_, err := Targets(res.Topological(), res.Files, genfile.Plugin{Files: []string{"a/*.proto", "nothing/*.proto", "b/**"}}, modfiles.WellKnown)
	if err == nil || !strings.Contains(err.Error(), `["nothing/*.proto" "b/**"] select no workspace file`) {
		t.Errorf("dead patterns: %v", err)
	}
	if got := all(genfile.Plugin{Files: []string{"a/a.proto"}, IncludeImports: true}); !slices.Equal(got, []string{"a/a.proto", "m1/m1.proto"}) {
		t.Errorf("imports: %v (the well-known empty.proto and descriptor.proto are never targets)", got)
	}
	if got := all(genfile.Plugin{IncludeImports: true}); !slices.Equal(got, []string{"a/a.proto", "a/b.proto", "m1/m1.proto"}) {
		t.Errorf("imports of all, each once: %v", got)
	}
	// A workspace file reached through another's import is a target
	// once, among the selected where selected, else after them.
	res2 := compileMods(t, []modfiles.Module{
		mod("example.com/a", "", true, map[string]string{
			"a/z.proto": "syntax = \"proto3\";\npackage a;\nimport \"a/y.proto\";\nmessage Z { Y y = 1; }\n",
			"a/y.proto": "syntax = \"proto3\";\npackage a;\nmessage Y {}\n",
		}),
	})
	if got, err := Targets(res2.Topological(), res2.Files, genfile.Plugin{Files: []string{"a/z.proto"}, IncludeImports: true}, modfiles.WellKnown); err != nil || !slices.Equal(got, []string{"a/z.proto", "a/y.proto"}) {
		t.Errorf("a workspace import: %v %v", got, err)
	}
	if got, err := Targets(res2.Topological(), res2.Files, genfile.Plugin{Files: []string{"a/z.proto"}}, modfiles.WellKnown); err != nil || !slices.Equal(got, []string{"a/z.proto"}) {
		t.Errorf("without imports: %v %v", got, err)
	}
	if got, err := Targets(res2.Topological(), res2.Files, genfile.Plugin{IncludeImports: true}, modfiles.WellKnown); err != nil || !slices.Equal(got, []string{"a/y.proto", "a/z.proto"}) {
		t.Errorf("a selected file reached by another's import stays once: %v %v", got, err)
	}
	_, err = Targets(res.Topological(), res.Files, genfile.Plugin{Files: []string{"m1/*.proto", "b/**"}}, modfiles.WellKnown)
	if err == nil || !strings.Contains(err.Error(), `["m1/*.proto" "b/**"] select no workspace file`) {
		t.Errorf("no match: %v", err)
	}
	_, err = Targets(res.Topological(), res.Files, genfile.Plugin{Files: []string{"a/*.proto", "b/**"}}, modfiles.WellKnown)
	if err == nil || !strings.Contains(err.Error(), `the pattern "b/**" selects no workspace file`) {
		t.Errorf("one dead pattern: %v", err)
	}
	// A workspace without files generates for nothing: the request
	// carries an empty list, as the spec's "every workspace file"
	// reads (REQ-gen-request).
	empty := compileMods(t, []modfiles.Module{mod("example.com/e", "", true, map[string]string{})})
	if got, err := Targets(empty.Topological(), empty.Files, genfile.Plugin{}, modfiles.WellKnown); err != nil || len(got) != 0 {
		t.Errorf("no workspace file: %v %v", got, err)
	}
	// The request carries the targets and their descriptors.
	req, err := Build(res.Topological(), res.Files, nil, genfile.Plugin{Files: []string{"a/b.proto"}, IncludeImports: true, Opt: "x"}, modfiles.WellKnown)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(req.GetFileToGenerate(), []string{"a/b.proto", "m1/m1.proto"}) || len(req.GetSourceFileDescriptors()) != 2 || req.GetSourceFileDescriptors()[1].GetName() != "m1/m1.proto" || req.GetParameter() != "x" {
		t.Errorf("request: %v %v %q", req.GetFileToGenerate(), req.GetSourceFileDescriptors(), req.GetParameter())
	}
}

// build is Build over a compiled result's own order, for an entry
// selecting every workspace file.
func build(res *compile.Result, overrides []genfile.Override, opt string) (*pluginpb.CodeGeneratorRequest, error) {
	return Build(res.Topological(), res.Files, overrides, genfile.Plugin{Opt: opt}, modfiles.WellKnown)
}

func mod(path, ver string, local bool, files map[string]string) modfiles.Module {
	m := modfiles.Module{Path: path, Version: ver, Local: local, Files: map[string][]byte{}}
	for p, b := range files {
		m.Files[p] = []byte(b)
	}
	return m
}

// The standard fixture: workspace module a (two files, BOTH importing
// the dependency — the diamond makes deduplication observable in
// proto_file), external m1 carrying custom FileOptions extensions.
func fixture(t testing.TB) *compile.Result {
	return compileMods(t, []modfiles.Module{
		mod("example.com/a", "", true, map[string]string{
			"a/a.proto": "syntax = \"proto3\";\npackage a;\nimport \"m1/m1.proto\";\nimport \"google/protobuf/empty.proto\";\nmessage A { m1.M m = 1; google.protobuf.Empty e = 2; }\n",
			"a/b.proto": "syntax = \"proto3\";\npackage a;\nimport \"m1/m1.proto\";\noption (m1.src_opt) = \"kept\";\nmessage B { m1.M m = 1; }\n",
		}),
		mod("example.com/m1", "v1.0.0", false, map[string]string{
			"m1/m1.proto": "syntax = \"proto2\";\npackage m1;\nimport \"google/protobuf/descriptor.proto\";\nextend google.protobuf.FileOptions { optional string my_opt = 50001; optional Conf my_conf = 50002; optional string src_opt = 50006 [retention = RETENTION_SOURCE]; }\nmessage First { message Deep { extend google.protobuf.FileOptions { optional string deep_opt = 50005; } } }\nextend google.protobuf.MessageOptions { optional string msg_opt = 50001; }\nextend google.protobuf.FileOptions { optional Kinds kinds = 50003; }\nmessage Kinds { optional int32 i32 = 1; optional sint32 s32 = 2; optional uint32 u32 = 3; optional uint64 u64 = 4; optional float f32 = 5; optional double f64 = 6; optional bytes by = 7; optional bool bl = 8; repeated string rep = 9; map<string,string> mp = 10; }\nmessage Wrapper { extend google.protobuf.FileOptions { optional string nested_opt = 50004; } }\nmessage Conf { optional int64 n = 1; optional bool b = 2; }\nmessage M {}\n",
		}),
	})
}

// file_to_generate is the workspace files in compile order; proto_file
// is topological with dependencies first; source_file_descriptors
// mirrors the generated files; parameter is verbatim; nothing else is
// set (REQ-gen-request).
func TestBuildShape(t *testing.T) {
	files := fixture(t)
	req, err := build(files, nil, "paths=source_relative")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(req.GetFileToGenerate(), []string{"a/a.proto", "a/b.proto"}) {
		t.Fatalf("file_to_generate = %v", req.GetFileToGenerate())
	}
	if req.GetParameter() != "paths=source_relative" {
		t.Fatalf("parameter = %q", req.GetParameter())
	}
	var names []string
	pos := map[string]int{}
	for i, fd := range req.GetProtoFile() {
		names = append(names, fd.GetName())
		pos[fd.GetName()] = i
	}
	for _, dep := range []string{"m1/m1.proto", "google/protobuf/empty.proto", "google/protobuf/descriptor.proto"} {
		if _, ok := pos[dep]; !ok {
			t.Fatalf("proto_file misses %s: %v", dep, names)
		}
	}
	if !(pos["google/protobuf/descriptor.proto"] < pos["m1/m1.proto"] && pos["m1/m1.proto"] < pos["a/a.proto"]) {
		t.Fatalf("not topological: %v", names)
	}
	if len(pos) != len(names) {
		t.Fatalf("proto_file has duplicate entries: %v", names)
	}
	if len(req.GetSourceFileDescriptors()) != 2 || req.GetSourceFileDescriptors()[0].GetName() != "a/a.proto" {
		t.Fatalf("source_file_descriptors = %v", req.GetSourceFileDescriptors())
	}
	if req.CompilerVersion != nil {
		t.Fatal("compiler_version set; the request carries nothing the spec does not name")
	}
	// A source-retention option is not stripped (REQ-gen-request).
	b := req.GetProtoFile()[pos["a/b.proto"]]
	if bb, _ := proto.Marshal(b.GetOptions()); !strings.Contains(string(bb), "kept") {
		t.Fatalf("the source-retention option was stripped: %v", b.GetOptions())
	}
	// An empty opt yields no parameter field at all.
	req2, err := build(files, nil, "")
	if err != nil || req2.Parameter != nil {
		t.Fatalf("empty opt: %v %v", req2.Parameter, err)
	}
}

func fileOpts(t *testing.T, req interface {
	GetProtoFile() []*descriptorpb.FileDescriptorProto
}, name string) *descriptorpb.FileOptions {
	t.Helper()
	for _, fd := range req.GetProtoFile() {
		if fd.GetName() == name {
			return fd.GetOptions()
		}
	}
	t.Fatalf("no %s", name)
	return nil
}

// Overrides apply to matching files in declaration order, later
// entries winning; dependency files match too; built-in, custom
// scalar, and custom message-field options all land
// (REQ-gen-overrides-declarative).
func TestOverridesApply(t *testing.T) {
	files := fixture(t)
	req, err := build(files, []genfile.Override{
		{Files: "a/*.proto", Option: "go_package", Value: "example.com/gen/first"},
		{Files: "a/a.proto", Option: "go_package", Value: "example.com/gen/second"},
		{Files: "**/*.proto", Option: "java_multiple_files", Value: "true"},
		{Files: "m1/m1.proto", Option: "(m1.my_opt)", Value: "hello"},
		{Files: "a/b.proto", Option: "(m1.my_conf).n", Value: "42"},
		{Files: "a/b.proto", Option: "(m1.my_conf).b", Value: "true"},
		{Files: "a/b.proto", Option: "optimize_for", Value: "CODE_SIZE"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := fileOpts(t, req, "a/a.proto").GetGoPackage(); got != "example.com/gen/second" {
		t.Fatalf("later entry did not win: %q", got)
	}
	if got := fileOpts(t, req, "a/b.proto").GetGoPackage(); got != "example.com/gen/first" {
		t.Fatalf("a/b.proto go_package = %q", got)
	}
	if !fileOpts(t, req, "m1/m1.proto").GetJavaMultipleFiles() {
		t.Fatal("dependency file not matched by **")
	}
	if fileOpts(t, req, "a/b.proto").GetOptimizeFor() != descriptorpb.FileOptions_CODE_SIZE {
		t.Fatal("enum by value name not applied")
	}
	m1opts := fileOpts(t, req, "m1/m1.proto").ProtoReflect()
	var sawOpt bool
	m1opts.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.IsExtension() && fd.FullName() == "m1.my_opt" {
			sawOpt = v.String() == "hello"
		}
		return true
	})
	if !sawOpt {
		t.Fatal("custom scalar extension not applied")
	}
	bopts := fileOpts(t, req, "a/b.proto").ProtoReflect()
	var n int64
	var b bool
	bopts.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.IsExtension() && fd.FullName() == "m1.my_conf" {
			msg := v.Message()
			n = msg.Get(msg.Descriptor().Fields().ByName("n")).Int()
			b = msg.Get(msg.Descriptor().Fields().ByName("b")).Bool()
		}
		return true
	})
	if n != 42 || !b {
		t.Fatalf("message-field extension: n=%d b=%v", n, b)
	}
}

// A derived override lands on every matched file through the request,
// spelled from the file's own path and package, later entries winning
// per file whichever kind they are, a file with no package getting
// nothing from a rule reading it, and a well-known import never
// touched (REQ-gen-overrides-derived, REQ-gen-overrides-declarative).
func TestDerive(t *testing.T) {
	// Through the request: a derived go_package lands on every matched
	// file from its own path and package, and a later plain value
	// still wins.
	files := fixture(t)
	req, err := build(files, []genfile.Override{
		{Files: "**", Option: "go_package", Prefix: "example.com/gen"},
		{Files: "**", Option: "java_package", Suffix: "pb"},
		{Files: "a/b.proto", Option: "go_package", Value: "example.com/plain"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := fileOpts(t, req, "a/a.proto").GetGoPackage(); got != "example.com/gen/a" {
		t.Errorf("a/a.proto go_package = %q", got)
	}
	if got := fileOpts(t, req, "m1/m1.proto").GetGoPackage(); got != "example.com/gen/m1" {
		t.Errorf("m1/m1.proto go_package = %q", got)
	}
	if got := fileOpts(t, req, "a/b.proto").GetGoPackage(); got != "example.com/plain" {
		t.Errorf("a/b.proto go_package = %q", got)
	}
	if got := fileOpts(t, req, "a/a.proto").GetJavaPackage(); got != "a.pb" {
		t.Errorf("a/a.proto java_package = %q", got)
	}
	if got := fileOpts(t, req, "google/protobuf/empty.proto").GetGoPackage(); got != "google.golang.org/protobuf/types/known/emptypb" {
		t.Errorf("well-known empty.proto go_package = %q, overridden", got)
	}
	// A plain value first and a derivation after: the derivation wins;
	// a derivation matching no file changes nothing.
	req, err = build(files, []genfile.Override{
		{Files: "**", Option: "go_package", Value: "example.com/plain"},
		{Files: "a/**", Option: "go_package", Prefix: "example.com/gen"},
		{Files: "nowhere/**", Option: "ruby_package", Suffix: "PB"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := fileOpts(t, req, "a/a.proto").GetGoPackage(); got != "example.com/gen/a" {
		t.Errorf("value then derived: a/a.proto go_package = %q", got)
	}
	if got := fileOpts(t, req, "m1/m1.proto").GetGoPackage(); got != "example.com/plain" {
		t.Errorf("value then derived: m1/m1.proto go_package = %q", got)
	}
	for _, f := range []string{"a/a.proto", "m1/m1.proto"} {
		if fileOpts(t, req, f).RubyPackage != nil {
			t.Errorf("%s: a derivation matching no file set ruby_package", f)
		}
	}
	// A file with no package gets nothing from a rule reading the
	// package: what it declares stays; go_package's prefix and
	// directory need no package.
	nopkg := compileMods(t, []modfiles.Module{mod("example.com/n", "", true, map[string]string{
		"n/n.proto": "syntax = \"proto3\";\noption java_package = \"declared\";\nmessage N {}\n",
	})})
	req, err = build(nopkg, []genfile.Override{
		{Files: "**", Option: "java_package", Suffix: "pb"},
		{Files: "**", Option: "ruby_package", Suffix: "PB"},
		{Files: "**", Option: "go_package", Prefix: "example.com/gen"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	opts := fileOpts(t, req, "n/n.proto")
	if opts.GetJavaPackage() != "declared" || opts.RubyPackage != nil || opts.GetGoPackage() != "example.com/gen/n" {
		t.Errorf("no package: java_package %q ruby_package %v go_package %q", opts.GetJavaPackage(), opts.RubyPackage, opts.GetGoPackage())
	}
}

// One entry's overrides never leak into another's request: each Build
// converts fresh descriptors (REQ-gen-request-determinism).
func TestBuildIsolation(t *testing.T) {
	files := fixture(t)
	withOv, err := build(files, []genfile.Override{{Files: "a/a.proto", Option: "go_package", Value: "example.com/leaky"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	clean, err := build(files, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if fileOpts(t, withOv, "a/a.proto").GetGoPackage() != "example.com/leaky" {
		t.Fatal("override missing where applied")
	}
	if fileOpts(t, clean, "a/a.proto").GetGoPackage() == "example.com/leaky" {
		t.Fatal("override leaked across Build calls")
	}
}

// Two Builds over the same inputs yield byte-identical requests, and
// the topological order is a pure function of the file set
// (REQ-gen-request-determinism).
// The request's proto_file, with no override, is the compiled
// result's descriptor set file for file: the schema a plugin is handed
// is the schema a build writes (REQ-gen-request, build.md
// REQ-build-set) — the well-known files with their options and source
// information among them; and a file to generate the order lacks is
// refused rather than carried as nothing.
func TestRequestCarriesTheDescriptorSet(t *testing.T) {
	res := fixture(t)
	req, err := build(res, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	set := res.DescriptorSet()
	if !proto.Equal(&descriptorpb.FileDescriptorSet{File: req.GetProtoFile()}, set) {
		t.Fatal("proto_file differs from the descriptor set")
	}
	for _, f := range set.File {
		if f.GetSourceCodeInfo() == nil {
			t.Fatalf("%s carries no source information", f.GetName())
		}
	}
	if _, err := Build(res.Topological()[:1], res.Files, nil, genfile.Plugin{}, modfiles.WellKnown); err == nil || !strings.Contains(err.Error(), "not in the compiled order") {
		t.Fatalf("a file outside the order: %v", err)
	}
}

func TestBuildDeterminism(t *testing.T) {
	files := fixture(t)
	ov := []genfile.Override{{Files: "**", Option: "go_package", Value: "example.com/x"}}
	a, err := build(files, ov, "p=1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := build(files, ov, "p=1")
	if err != nil {
		t.Fatal(err)
	}
	ab, _ := proto.MarshalOptions{Deterministic: true}.Marshal(a)
	bb, _ := proto.MarshalOptions{Deterministic: true}.Marshal(b)
	if !slices.Equal(ab, bb) {
		t.Fatal("requests differ across identical Builds")
	}
	rapid.Check(t, func(rt *rapid.T) {
		// Shuffling the workspace file *contents map* cannot matter (maps
		// are unordered); shuffle the top-level Files slice instead and
		// require the same proto_file order modulo the generated files'
		// positions.
		perm := linker.Files(rapid.Permutation(slices.Clone([]linker.File(files.Files))).Draw(rt, "perm"))
		req, err := Build(compile.Topological(perm), perm, nil, genfile.Plugin{}, modfiles.WellKnown)
		if err != nil {
			rt.Fatal(err)
		}
		var deps []string
		for _, fd := range req.GetProtoFile() {
			deps = append(deps, fd.GetName())
		}
		pos := map[string]int{}
		for i, n := range deps {
			pos[n] = i
		}
		if !(pos["google/protobuf/descriptor.proto"] < pos["m1/m1.proto"] && pos["m1/m1.proto"] < pos["a/a.proto"]) {
			rt.Fatalf("not topological under permutation: %v", deps)
		}
	})
}

// Every scalar kind parses its spelling and rejects a bad one; the
// nested extension declaration resolves; repeated and map fields
// refuse (REQ-gen-overrides-declarative's per-kind clause).
func TestOverrideKindMatrix(t *testing.T) {
	files := fixture(t)
	good := map[string]string{
		"(m1.kinds).i32":           "-7",
		"(m1.kinds).s32":           "8",
		"(m1.kinds).u32":           "9",
		"(m1.kinds).u64":           "10",
		"(m1.kinds).f32":           "1.5",
		"(m1.kinds).f64":           "2.25",
		"(m1.kinds).by":            "raw",
		"(m1.kinds).bl":            "false",
		"(m1.Wrapper.nested_opt)":  "deep",
		"(m1.First.Deep.deep_opt)": "deeper",
	}
	var ovs []genfile.Override
	for opt, v := range good {
		ovs = append(ovs, genfile.Override{Files: "a/b.proto", Option: opt, Value: v})
	}
	req, err := build(files, ovs, "")
	if err != nil {
		t.Fatal(err)
	}
	opts := fileOpts(t, req, "a/b.proto").ProtoReflect()
	got := map[string]string{}
	opts.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if !fd.IsExtension() {
			return true
		}
		switch fd.FullName() {
		case "m1.Wrapper.nested_opt":
			got["nested"] = v.String()
		case "m1.First.Deep.deep_opt":
			got["deep2"] = v.String()
		case "m1.kinds":
			msg := v.Message()
			flds := msg.Descriptor().Fields()
			for _, n := range []string{"i32", "s32", "u32", "u64", "f32", "f64", "bl"} {
				fd := flds.ByName(protoreflect.Name(n))
				if msg.Has(fd) {
					got[n] = msg.Get(fd).String()
				}
			}
			if by := flds.ByName("by"); msg.Has(by) {
				got["by"] = string(msg.Get(by).Bytes())
			}
		}
		return true
	})
	want := map[string]string{"i32": "-7", "s32": "8", "u32": "9", "u64": "10", "f32": "1.5", "f64": "2.25", "by": "raw", "bl": "false", "nested": "deep", "deep2": "deeper"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	bad := map[string]string{
		// "a" is a digit in base 11 and up: a mutated parse base would
		// accept it; the 2^31/2^32 rows sit one past the bit width, so a
		// widened bitSize would accept and silently truncate.
		"(m1.kinds).i32": "a",
		"(m1.kinds).s32": "4.5",
		"(m1.kinds).u32": "a",
		"(m1.kinds).u64": "a",
		"(m1.kinds).f32": "x",
		"(m1.kinds).f64": "x",
		"(m1.kinds).bl":  "0",
	}
	bad["(m1.kinds).i32+range"] = ""
	delete(bad, "(m1.kinds).i32+range")
	for opt, v := range bad {
		if _, err := build(files, []genfile.Override{{Files: "a/b.proto", Option: opt, Value: v}}, ""); err == nil {
			t.Errorf("%s=%q accepted", opt, v)
		}
	}
	for opt, v := range map[string]string{
		"(m1.kinds).i32": "2147483648",
		"(m1.kinds).u32": "4294967296",
		"(m1.kinds).f32": "3.5e38",
	} {
		if _, err := build(files, []genfile.Override{{Files: "a/b.proto", Option: opt, Value: v}}, ""); err == nil {
			t.Errorf("%s=%q accepted beyond the bit width", opt, v)
		}
	}
	for _, opt := range []string{"(m1.kinds).rep", "(m1.kinds).mp"} {
		_, err := build(files, []genfile.Override{{Files: "a/b.proto", Option: opt, Value: "a"}}, "")
		if err == nil || !strings.Contains(err.Error(), "repeated and map options are not overridable") {
			t.Errorf("%s: err = %v, want the repeated-and-map refusal", opt, err)
		}
	}
}

// Failure arms: unknown option, unknown extension, extension of the
// wrong extendee, non-scalar targets, bad values, unknown field of a
// message extension (REQ-gen-overrides-declarative's fail-never-guess
// clause).
func TestOverrideFailures(t *testing.T) {
	files := fixture(t)
	cases := []struct {
		name  string
		o     genfile.Override
		wants string
	}{
		{"unknown option", genfile.Override{Files: "a/a.proto", Option: "no_such_option", Value: "x"}, "no such file option"},
		{"unknown extension", genfile.Override{Files: "a/a.proto", Option: "(m1.nope)", Value: "x"}, "no extension m1.nope"},
		{"message name is not an extension", genfile.Override{Files: "a/a.proto", Option: "(m1.M)", Value: "x"}, "no extension"},
		{"wrong extendee", genfile.Override{Files: "a/a.proto", Option: "(m1.msg_opt)", Value: "x"}, "extends google.protobuf.MessageOptions, not FileOptions"},
		{"unclosed extension via Build", genfile.Override{Files: "a/a.proto", Option: "(m1.broken", Value: "x"}, "unclosed extension name"},
		{"message-valued", genfile.Override{Files: "a/a.proto", Option: "(m1.my_conf)", Value: "x"}, "not overridable"},
		{"bad bool", genfile.Override{Files: "a/a.proto", Option: "java_multiple_files", Value: "yes"}, "not true or false"},
		{"bad enum", genfile.Override{Files: "a/a.proto", Option: "optimize_for", Value: "TINY"}, "not a value of"},
		{"bad int", genfile.Override{Files: "a/b.proto", Option: "(m1.my_conf).n", Value: "4.5"}, "invalid syntax"},
		{"unknown field", genfile.Override{Files: "a/b.proto", Option: "(m1.my_conf).zz", Value: "1"}, `has no field "zz"`},
		{"scalar with field", genfile.Override{Files: "a/b.proto", Option: "(m1.my_opt).x", Value: "1"}, "not message-typed"},
		{"features message", genfile.Override{Files: "a/a.proto", Option: "features", Value: "x"}, "not overridable"},
	}
	for _, tc := range cases {
		_, err := build(files, []genfile.Override{tc.o}, "")
		if err == nil || !strings.Contains(err.Error(), tc.wants) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.wants)
		}
	}
}

package env1

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/wellknownimports"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"pgregory.net/rapid"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/check/rules"
)

// The fixture: three packages over four files — a proto3 file with a
// public import, a proto2 dependency, an editions file with custom
// options — and a package cycle a → c → a through distinct files.
var fixture = map[string]string{
	"a/a.proto": `syntax = "proto3";
package a;

import "google/protobuf/descriptor.proto";
import public "b/b.proto";
import "c/c.proto";
import "a/y.proto";

option (c.file_opt) = "x";

// detached one

// detached two

// Outer leads.
message Outer { // Outer trails.
  // name leads.
  string name = 1; // name trails.
  map<string, int32> counts = 2;
  oneof choice {
    int32 x = 3;
    b.Thing thing = 4;
  }
  optional int32 opt = 5;
  message Inner {
    enum Kind {
      KIND_UNSPECIFIED = 0;
      KIND_A = 1;
    }
    Kind kind = 1 [deprecated = true, (c.field_opt) = {tag: "t", nums: [1, 2]}];
  }
  Inner inner = 6;
  Y y = 7;
  repeated string tags = 8;
}

enum Color {
  COLOR_UNSPECIFIED = 0;
}

service Svc {
  rpc Do(Outer) returns (b.Thing);
}
`,
	"a/y.proto": `syntax = "proto3";
package a;
message Y {}
`,
	"b/b.proto": `syntax = "proto2";
package b;
import public "d/d.proto";
message Thing {
  optional string id = 1;
  required int32 req = 2;
  optional group G = 3 {
    optional int32 v = 1;
  }
  repeated int32 packed = 4 [packed = true];
  repeated int32 expanded = 5;
  repeated string strs = 6;
  extensions 100 to 200;
}
extend Thing {
  optional string ext = 100;
}
`,
	"d/d.proto": `syntax = "proto3";
package d;
message D {}
`,
	"e/e.proto": `edition = "2023";
package e;
option features.field_presence = IMPLICIT;
message E {
  int32 n = 1;
  E child = 2;
}
`,
	"c/c.proto": `edition = "2023";
package c;

import "google/protobuf/descriptor.proto";
import "a/y.proto";

message FieldOpt {
  string tag = 1;
  repeated int32 nums = 2;
}
extend google.protobuf.FieldOptions {
  FieldOpt field_opt = 50001;
}
extend google.protobuf.FileOptions {
  string file_opt = 50002;
}
message Ed {
  string s = 1 [features.field_presence = IMPLICIT];
  a.Y y = 2;
  repeated int32 r = 3 [features.repeated_field_encoding = EXPANDED];
  FieldOpt delimited = 4 [features.message_encoding = DELIMITED];
  int32 must = 5 [features.field_presence = LEGACY_REQUIRED];
}
`,
}

func compileSet(t *testing.T, srcs map[string]string) *Set {
	t.Helper()
	c := protocompile.Compiler{
		Resolver:       wellknownimports.WithStandardImports(&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(srcs)}),
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	names := make([]string, 0, len(srcs))
	for n := range srcs {
		names = append(names, n)
	}
	sort.Strings(names)
	files, err := c.Compile(context.Background(), names...)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return NewSet(files)
}

func lintEnv(t *testing.T) (*Env, *Set) {
	t.Helper()
	set := compileSet(t, fixture)
	env, err := New(set, nil)
	if err != nil {
		t.Fatal(err)
	}
	return env, set
}

// tb is what the helpers need of a test, testing's or rapid's.
type tb interface {
	Helper()
	Fatal(...any)
	Fatalf(string, ...any)
	Errorf(string, ...any)
}

// verdict compiles and evaluates one rule expression.
func verdict(t tb, env *Env, kind check.Kind, target check.Target, expr string, vars map[string]any) (bool, error) {
	t.Helper()
	prg, err := env.Compile(rules.Rule{ID: "T", Kind: kind, Target: target, Severity: check.SeverityError, CEL: expr, Message: "m"})
	if err != nil {
		return false, err
	}
	return prg.Eval(vars)
}

// holds fails the test unless the lint rule holds over the bindings.
func holds(t tb, env *Env, target check.Target, expr string, vars map[string]any) {
	t.Helper()
	ok, err := verdict(t, env, check.KindLint, target, expr, vars)
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	if !ok {
		t.Errorf("%s: false", expr)
	}
}

// refusedAtCompile fails the test unless the rule is refused at
// compile time: a kind the function is not declared over, statically
// typed.
func refusedAtCompile(t tb, env *Env, target check.Target, expr string, vars map[string]any) {
	t.Helper()
	_, err := verdict(t, env, check.KindLint, target, expr, vars)
	if err == nil || !errors.Is(err, ErrCompile) {
		t.Errorf("%s: %v", expr, err)
	}
}

// The fixture's protos by name, for bindings.
type protos struct {
	a, y, b, c *descriptorpb.FileDescriptorProto
	outer      *descriptorpb.DescriptorProto
	inner      *descriptorpb.DescriptorProto
	name       *descriptorpb.FieldDescriptorProto
	kind       *descriptorpb.FieldDescriptorProto
	edS        *descriptorpb.FieldDescriptorProto
	thingID    *descriptorpb.FieldDescriptorProto
	kindEnum   *descriptorpb.EnumDescriptorProto
}

func fixtureProtos(set *Set) protos {
	p := protos{a: set.File("a/a.proto"), y: set.File("a/y.proto"), b: set.File("b/b.proto"), c: set.File("c/c.proto")}
	p.outer = p.a.MessageType[0]
	p.name = p.outer.Field[0]
	p.inner = p.outer.NestedType[1] // the map entry is nested first
	p.kindEnum = p.inner.EnumType[0]
	p.kind = p.inner.Field[0]
	p.edS = p.c.MessageType[1].Field[0]
	p.thingID = p.b.MessageType[0].Field[0]
	return p
}

// Every kind and target declares exactly its bindings: an entity under
// its target's name (enumValue for enum-value) beside file; package
// and files for a package rule; files for a set rule; the old/new
// forms for breaking, each nullable; a name of another target is
// unknown; a breaking rule is refused under a lint environment and a
// non-boolean expression under any (REQ-env1-bindings).
func TestBindings(t *testing.T) {
	env, _ := lintEnv(t)
	cases := []struct {
		target check.Target
		expr   string
		ok     bool
	}{
		{check.TargetFile, "file.name != '' && file.package != ''", true},
		{check.TargetFile, "message.name != ''", false},
		{check.TargetPackage, "pkg.startsWith('a') && files.size() > 0", true},
		{check.TargetPackage, "package.startsWith('a')", false},
		{check.TargetPackage, "file.name != ''", false},
		{check.TargetSet, "files.size() > 0", true},
		{check.TargetSet, "pkg != ''", false},
		{check.TargetMessage, "message.name != '' && file.name != ''", true},
		{check.TargetField, "field.number > 0 && file.name != ''", true},
		{check.TargetOneof, "oneof.name != ''", true},
		{check.TargetEnum, "enum.value.size() > 0", true},
		{check.TargetEnumValue, "enumValue.number >= 0", true},
		{check.TargetEnumValue, "enum-value.number >= 0", false},
		{check.TargetService, "service.method.size() > 0", true},
		{check.TargetMethod, "method.input_type != ''", true},
		{check.TargetExtension, "extension.extendee != ''", true},
		{check.TargetField, "field.name", false},
		{check.TargetField, "field.name.size()", false},
		{check.TargetField, "field.nonesuch == 1", false},
	}
	for _, c := range cases {
		_, err := env.Compile(rules.Rule{ID: "T", Kind: check.KindLint, Target: c.target, CEL: c.expr})
		if (err == nil) != c.ok {
			t.Errorf("lint %s %q: %v", c.target, c.expr, err)
		}
		if err != nil && !errors.Is(err, ErrCompile) {
			t.Errorf("lint %s %q: %v is not ErrCompile", c.target, c.expr, err)
		}
	}
	if _, err := env.Compile(rules.Rule{ID: "B", Kind: check.KindBreaking, Target: check.TargetField, CEL: "true"}); err == nil || !strings.Contains(err.Error(), "breaking rule under a lint environment") {
		t.Errorf("breaking under lint: %v", err)
	}
	set := compileSet(t, fixture)
	benv, err := New(set, compileSet(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	bcases := []struct {
		target check.Target
		expr   string
		ok     bool
	}{
		{check.TargetField, "old == null || new == null || old.number == new.number", true},
		{check.TargetField, "oldFile == null || newFile.name == oldFile.name", true},
		{check.TargetField, "field.number > 0", false},
		{check.TargetPackage, "oldPackage == null || newPackage == oldPackage", true},
		{check.TargetPackage, "oldFiles == null || newFiles.size() >= oldFiles.size()", true},
		{check.TargetSet, "oldFiles.size() <= newFiles.size()", true},
		{check.TargetSet, "old == null", false},
		{check.TargetFile, "old == null || new == null || old.name == new.name", true},
		{check.TargetFile, "oldFile == null", false},
	}
	for _, c := range bcases {
		_, err := benv.Compile(rules.Rule{ID: "T", Kind: check.KindBreaking, Target: c.target, CEL: c.expr})
		if (err == nil) != c.ok {
			t.Errorf("breaking %s %q: %v", c.target, c.expr, err)
		}
	}
	if _, err := benv.Compile(rules.Rule{ID: "L", Kind: check.KindLint, Target: check.TargetField, CEL: "true"}); err == nil {
		t.Error("lint under breaking compiled")
	}
}

// A verdict is the expression's boolean; a runtime error, a foreign
// descriptor and an absent binding are failures, never verdicts
// (REQ-rules-verdict).
func TestEvalVerdicts(t *testing.T) {
	env, set := lintEnv(t)
	p := fixtureProtos(set)
	vars := map[string]any{"field": p.name, "file": p.a}
	if ok, err := verdict(t, env, check.KindLint, check.TargetField, "field.name == 'name'", vars); err != nil || !ok {
		t.Fatalf("true: %v %v", ok, err)
	}
	if ok, err := verdict(t, env, check.KindLint, check.TargetField, "field.name == 'other'", vars); err != nil || ok {
		t.Fatalf("false: %v %v", ok, err)
	}
	if _, err := verdict(t, env, check.KindLint, check.TargetField, "field.name == 'name'", map[string]any{"file": p.a}); err == nil || !errors.Is(err, ErrEval) {
		t.Fatalf("absent binding: %v", err)
	}
	if _, err := verdict(t, env, check.KindLint, check.TargetField, "[1][2] == 1", vars); err == nil || !errors.Is(err, ErrEval) {
		t.Fatalf("runtime error: %v", err)
	}
	foreign := &descriptorpb.FieldDescriptorProto{Name: str("name")}
	if _, err := verdict(t, env, check.KindLint, check.TargetField, "parent(field) != null", map[string]any{"field": foreign, "file": p.a}); err == nil || !strings.Contains(err.Error(), "no declaration of the compiled schema") {
		t.Fatalf("foreign descriptor: %v", err)
	}
	// A foreign proto is still a proto: its fields are readable, only
	// the library refuses it.
	if ok, err := verdict(t, env, check.KindLint, check.TargetField, "field.name == 'name'", map[string]any{"field": foreign, "file": p.a}); err != nil || !ok {
		t.Fatalf("foreign fields: %v %v", ok, err)
	}
}

func str(s string) *string { return &s }

// The library over the fixture (REQ-env1-library).
func TestLibrary(t *testing.T) {
	env, set := lintEnv(t)
	p := fixtureProtos(set)
	field := map[string]any{"field": p.name, "file": p.a}
	kind := map[string]any{"field": p.kind, "file": p.a}
	msg := map[string]any{"message": p.outer, "file": p.a}
	inner := map[string]any{"message": p.inner, "file": p.a}
	file := map[string]any{"file": p.a}
	cfile := map[string]any{"file": p.c}
	enum := map[string]any{"enum": p.kindEnum, "file": p.a}
	value := map[string]any{"enumValue": p.kindEnum.Value[1], "file": p.a}
	method := map[string]any{"method": p.a.Service[0].Method[0], "file": p.a}
	files := map[string]any{"files": []*descriptorpb.FileDescriptorProto{p.a, p.y, p.c}}

	// comments
	holds(t, env, check.TargetField, `comments(field) == {'leading': ' name leads.\n', 'trailing': ' name trails.\n', 'detached': []}`, field)
	holds(t, env, check.TargetMessage, `comments(message).leading == ' Outer leads.\n' && comments(message).trailing == ' Outer trails.\n' && comments(message).detached == [' detached one\n', ' detached two\n']`, msg)
	holds(t, env, check.TargetField, `comments(field).leading == '' && dyn(comments(parent(file))) == null`, kind)
	// parent
	holds(t, env, check.TargetField, `parent(field) == messages(file)[0] && parent(parent(field)) == file && parent(file) == null && parent(null) == null`, field)
	holds(t, env, check.TargetField, `parent(field).name == 'Inner' && parent(parent(field)).name == 'Outer'`, kind)
	holds(t, env, check.TargetEnumValue, `parent(enumValue).name == 'Kind' && parent(parent(enumValue)).name == 'Inner'`, value)
	holds(t, env, check.TargetMethod, `parent(method).name == 'Svc' && parent(parent(method)) == file`, method)
	// file, fullName
	holds(t, env, check.TargetField, `file(field) == file && dyn(file(parent(file))) == null && fullName(field) == 'a.Outer.name' && fullName(file) == 'a/a.proto' && dyn(fullName(parent(file))) == null`, field)
	holds(t, env, check.TargetEnumValue, `fullName(enumValue) == 'a.Outer.Inner.KIND_A' && file(enumValue).name == 'a/a.proto'`, value)
	holds(t, env, check.TargetEnum, `fullName(enum) == 'a.Outer.Inner.Kind' && parent(enum).name == 'Inner'`, enum)
	refusedAtCompile(t, env, check.TargetEnum, `enums(enum).size() == 0`, enum)
	// declarations under a file, a list of files, a message
	holds(t, env, check.TargetFile, `messages(file).map(m, m.name) == ['Outer', 'Inner'] && enums(file).map(e, e.name) == ['Color', 'Kind'] && extensions(file) == [] && services(file).map(s, s.name) == ['Svc']`, file)
	holds(t, env, check.TargetFile, `extensions(file).map(x, fullName(x)) == ['b.ext'] && messages(file).map(m, m.name) == ['Thing', 'G']`, map[string]any{"file": p.b})
	holds(t, env, check.TargetMessage, `messages(message).map(m, m.name) == ['Inner'] && enums(message).map(e, e.name) == ['Kind'] && extensions(message) == [] && services(message) == []`, msg)
	holds(t, env, check.TargetMessage, `messages(message) == [] && enums(message).map(e, e.name) == ['Kind']`, inner)
	holds(t, env, check.TargetSet, `messages(files).map(m, m.name) == ['Outer', 'Inner', 'Y', 'FieldOpt', 'Ed'] && extensions(files).map(x, fullName(x)) == ['c.field_opt', 'c.file_opt'] && messages([]) == [] && dyn(messages(parent(files[0]))) == null`, files)
	refusedAtCompile(t, env, check.TargetField, `messages(field).size() == 0`, field)
	refusedAtCompile(t, env, check.TargetMessage, `imports(message).size() == 0`, msg)
	if _, err := verdict(t, env, check.KindLint, check.TargetField, `imports(parent(field)) == []`, field); err == nil || !errors.Is(err, ErrEval) || !strings.Contains(err.Error(), "no such overload") {
		t.Errorf("imports over a dynamic message: %v", err)
	}
	// resolve, fileByName
	holds(t, env, check.TargetFile, `resolve('a.Outer') == messages(file)[0] && resolve('.a.Outer.name').number == 1 && resolve('a.Outer.Inner.KIND_A').number == 1 && resolve('a.Svc.Do').name == 'Do' && resolve('b.ext').number == 100 && resolve('a.Color').name == 'Color' && resolve('nonesuch') == null && resolve('google.protobuf.FieldOptions').name == 'FieldOptions' && resolve('a.Outer.choice').name == 'choice' && resolve('a.Outer.CountsEntry') == null && parent(file.message_type[0].nested_type[0].field[0]).name == 'CountsEntry' && parent(parent(file.message_type[0].nested_type[0].field[0])).name == 'Outer'`, file)
	holds(t, env, check.TargetFile, `fileByName('b/b.proto').package == 'b' && fileByName('google/protobuf/descriptor.proto') != null && fileByName('nonesuch.proto') == null`, file)
	// imports, visible
	holds(t, env, check.TargetFile, `imports(file).map(f, f.name) == ['google/protobuf/descriptor.proto', 'b/b.proto', 'c/c.proto', 'a/y.proto'] && dyn(imports(parent(file))) == null`, file)
	// Direct imports first, in order; then what they make visible
	// through public imports, transitively.
	holds(t, env, check.TargetFile, `visible(file).map(f, f.name) == ['a/a.proto', 'google/protobuf/descriptor.proto', 'b/b.proto', 'c/c.proto', 'a/y.proto', 'd/d.proto']`, file)
	holds(t, env, check.TargetFile, `visible(file).map(f, f.name) == ['b/b.proto', 'd/d.proto'] && imports(file).map(f, f.name) == ['d/d.proto']`, map[string]any{"file": p.b})
	// references
	holds(t, env, check.TargetFile, `references(file) == ['c.file_opt', 'b.Thing', 'a.Outer.Inner', 'a.Y', 'a.Outer.Inner.Kind', 'c.field_opt', 'a.Outer']`, file)
	holds(t, env, check.TargetFile, `references(file) == ['a.Y', 'c.FieldOpt', 'google.protobuf.FieldOptions', 'google.protobuf.FileOptions']`, cfile)
	holds(t, env, check.TargetFile, `references(file) == []`, map[string]any{"file": p.y})
	// features
	holds(t, env, check.TargetField, `features(field).field_presence == google.protobuf.FeatureSet.FieldPresence.IMPLICIT && features(field).enum_type == google.protobuf.FeatureSet.EnumType.OPEN`, field)
	holds(t, env, check.TargetField, `features(field).field_presence == google.protobuf.FeatureSet.FieldPresence.EXPLICIT && features(field).enum_type == google.protobuf.FeatureSet.EnumType.CLOSED && features(field).message_encoding == google.protobuf.FeatureSet.MessageEncoding.LENGTH_PREFIXED`, map[string]any{"field": p.thingID, "file": p.b})
	holds(t, env, check.TargetField, `features(field).field_presence == google.protobuf.FeatureSet.FieldPresence.IMPLICIT && features(file).field_presence == google.protobuf.FeatureSet.FieldPresence.EXPLICIT && dyn(features(parent(file))) == null`, map[string]any{"field": p.edS, "file": p.c})
	// A field's legacy modifiers are the features they imply: proto3
	// optional is explicit presence, required is legacy required, a
	// group is delimited, packed is as written and proto3's default
	// (REQ-env1-library).
	thing := p.b.MessageType[0]
	fieldFeature := func(f *descriptorpb.FieldDescriptorProto, file *descriptorpb.FileDescriptorProto, expr string) {
		t.Helper()
		holds(t, env, check.TargetField, expr, map[string]any{"field": f, "file": file})
	}
	fieldFeature(p.outer.Field[4], p.a, `field.name == 'opt' && features(field).field_presence == google.protobuf.FeatureSet.FieldPresence.EXPLICIT`)
	fieldFeature(p.outer.Field[0], p.a, `features(field).field_presence == google.protobuf.FeatureSet.FieldPresence.IMPLICIT`)
	fieldFeature(p.outer.Field[2], p.a, `field.name == 'x' && field.oneof_index == 0 && features(field).field_presence == google.protobuf.FeatureSet.FieldPresence.EXPLICIT`)
	fieldFeature(thing.Field[1], p.b, `field.name == 'req' && features(field).field_presence == google.protobuf.FeatureSet.FieldPresence.LEGACY_REQUIRED`)
	fieldFeature(thing.Field[2], p.b, `field.name == 'g' && features(field).message_encoding == google.protobuf.FeatureSet.MessageEncoding.DELIMITED && features(field).field_presence == google.protobuf.FeatureSet.FieldPresence.EXPLICIT`)
	fieldFeature(thing.Field[3], p.b, `field.name == 'packed' && features(field).repeated_field_encoding == google.protobuf.FeatureSet.RepeatedFieldEncoding.PACKED`)
	fieldFeature(thing.Field[4], p.b, `field.name == 'expanded' && features(field).repeated_field_encoding == google.protobuf.FeatureSet.RepeatedFieldEncoding.EXPANDED`)
	fieldFeature(thing.Field[5], p.b, `field.name == 'strs' && features(field).repeated_field_encoding == google.protobuf.FeatureSet.RepeatedFieldEncoding.EXPANDED`)
	fieldFeature(p.outer.Field[1], p.a, `field.name == 'counts' && features(field).repeated_field_encoding == google.protobuf.FeatureSet.RepeatedFieldEncoding.PACKED`)
	fieldFeature(p.c.MessageType[1].Field[2], p.c, `field.name == 'r' && features(field).repeated_field_encoding == google.protobuf.FeatureSet.RepeatedFieldEncoding.EXPANDED`)
	fieldFeature(p.c.MessageType[1].Field[1], p.c, `field.name == 'y' && features(field).repeated_field_encoding == google.protobuf.FeatureSet.RepeatedFieldEncoding.PACKED && features(field).field_presence == google.protobuf.FeatureSet.FieldPresence.EXPLICIT`)
	// A message-typed field has presence whatever the feature says, in
	// every syntax: the feature reports the resolved value — implicit
	// under an editions file's implicit default as under proto3 — and
	// presence is read from the kind.
	e := set.File("e/e.proto")
	fieldFeature(e.MessageType[0].Field[0], e, `field.name == 'n' && features(field).field_presence == google.protobuf.FeatureSet.FieldPresence.IMPLICIT`)
	fieldFeature(e.MessageType[0].Field[1], e, `field.name == 'child' && features(field).field_presence == google.protobuf.FeatureSet.FieldPresence.IMPLICIT`)
	fieldFeature(p.outer.Field[5], p.a, `field.name == 'inner' && features(field).field_presence == google.protobuf.FeatureSet.FieldPresence.IMPLICIT`)
	// A proto3 repeated string is not packable: the file's feature
	// stands. An editions field's modifiers are its features already.
	fieldFeature(p.outer.Field[7], p.a, `field.name == 'tags' && features(field).repeated_field_encoding == google.protobuf.FeatureSet.RepeatedFieldEncoding.PACKED`)
	fieldFeature(p.c.MessageType[1].Field[3], p.c, `field.name == 'delimited' && features(field).message_encoding == google.protobuf.FeatureSet.MessageEncoding.DELIMITED`)
	fieldFeature(p.c.MessageType[1].Field[4], p.c, `field.name == 'must' && features(field).field_presence == google.protobuf.FeatureSet.FieldPresence.LEGACY_REQUIRED`)
	// options
	holds(t, env, check.TargetField, `options(field) == {'deprecated': true, '(c.field_opt)': {'tag': 't', 'nums': [1, 2]}} && options(field).deprecated && options(field)['(c.field_opt)'].nums[1] == 2`, kind)
	holds(t, env, check.TargetField, `options(field) == {} && dyn(options(parent(file))) == null`, field)
	holds(t, env, check.TargetFile, `options(file) == {'(c.file_opt)': 'x'}`, file)
	holds(t, env, check.TargetField, `options(field) == {'packed': true}`, map[string]any{"field": thing.Field[3], "file": p.b})
	holds(t, env, check.TargetField, `options(field) == {'features': {'field_presence': 'IMPLICIT'}}`, map[string]any{"field": p.edS, "file": p.c})
	// words, case
	holds(t, env, check.TargetField, `words('getHTTPResponse2Code') == ['get', 'HTTP', 'Response2', 'Code'] && case('get_http_response', 'pascal') == 'GetHttpResponse' && case('HTTPServer', 'camel') == 'httpServer' && case('fooBar', 'upper-snake') == 'FOO_BAR' && case('FooBar', 'snake') == 'foo_bar'`, field)
	if _, err := verdict(t, env, check.KindLint, check.TargetField, `case('x', 'kebab') == 'x'`, field); err == nil || !strings.Contains(err.Error(), "no style") {
		t.Errorf("unknown style: %v", err)
	}
	// packageCycles
	holds(t, env, check.TargetSet, `packageCycles(files) == [['a', 'c']] && packageCycles([]) == []`, files)
	// a/a.proto imports a/y.proto: an import within a package is no
	// edge, so a package alone never cycles.
	holds(t, env, check.TargetSet, `packageCycles(files) == []`, map[string]any{"files": []*descriptorpb.FileDescriptorProto{p.a, p.y}})
	// strings and lists extensions, the standard library
	holds(t, env, check.TargetField, `field.name.upperAscii() == 'NAME' && [3, 1, 2].sort() == [1, 2, 3] && 'a,b'.split(',').size() == 2`, field)
}

// A rule quadratic in the schema exceeds the cost limit and fails
// naming it; a rule linear in the schema fits; a lookup by name is
// charged the schema's size (REQ-rules-bounded).
func TestBounded(t *testing.T) {
	env, set := lintEnv(t)
	p := fixtureProtos(set)
	all := make([]*descriptorpb.FileDescriptorProto, 0, len(set.files))
	for _, f := range set.files {
		all = append(all, f.proto)
	}
	files := map[string]any{"files": all}
	holds(t, env, check.TargetSet, `files.all(f, messages(f).all(m, m.field.all(x, x.name != 'zzz')))`, files)
	cubic := `lists.range(1000).all(a, lists.range(1000).all(b, lists.range(1000).all(c, files.size() > 0)))`
	if _, err := verdict(t, env, check.KindLint, check.TargetSet, cubic, files); err == nil || !errors.Is(err, ErrEval) || !strings.Contains(err.Error(), "cost limit") {
		t.Fatalf("cubic: %v", err)
	}
	prg, err := env.Compile(rules.Rule{ID: "R", Kind: check.KindLint, Target: check.TargetFile, CEL: `resolve('a.Outer') != null`})
	if err != nil {
		t.Fatal(err)
	}
	_, details, err := prg.eval(map[string]any{"file": p.a})
	if err != nil {
		t.Fatal(err)
	}
	if cost := details.ActualCost(); cost == nil || *cost < uint64(set.Size()) {
		t.Fatalf("resolve cost %v < schema size %d", cost, set.Size())
	}
	if limit := costLimit(set.Size()); env.limit != limit || limit <= costBase {
		t.Fatalf("limit = %d", env.limit)
	}
}

// Under a breaking environment a function on an entity finds the
// entity's own side, and a lookup by name searches the new side.
func TestBreakingSides(t *testing.T) {
	oldSrc := map[string]string{}
	for k, v := range fixture {
		oldSrc[k] = v
	}
	oldSrc["a/a.proto"] = strings.Replace(fixture["a/a.proto"], "string name = 1;", "string name = 1;\n  string gone = 9;", 1)
	oldSet, newSet := compileSet(t, oldSrc), compileSet(t, fixture)
	env, err := New(newSet, oldSet)
	if err != nil {
		t.Fatal(err)
	}
	op, np := fixtureProtos(oldSet), fixtureProtos(newSet)
	gone := op.outer.Field[1]
	vars := map[string]any{"old": gone, "new": nil, "oldFile": op.a, "newFile": np.a}
	ok, err := verdict(t, env, check.KindBreaking, check.TargetField, `new == null && file(old) == oldFile && file(old) != newFile && parent(old).field.size() == 9 && resolve('a.Outer').field.size() == 8 && resolve('a.Outer.gone') == null && fileByName('a/a.proto') == newFile`, vars)
	if err != nil || !ok {
		t.Fatalf("sides: %v %v", ok, err)
	}
	pair := map[string]any{"old": op.name, "new": np.name, "oldFile": op.a, "newFile": np.a}
	ok, err = verdict(t, env, check.KindBreaking, check.TargetField, `file(old) == oldFile && file(new) == newFile && parent(old).field.size() == 9 && parent(new).field.size() == 8 && fullName(old) == fullName(new)`, pair)
	if err != nil || !ok {
		t.Fatalf("pair: %v %v", ok, err)
	}
	ok, err = verdict(t, env, check.KindBreaking, check.TargetPackage, `oldPackage == 'a' && newPackage == null && oldFiles.size() == 1 && newFiles == null`, map[string]any{"oldPackage": "a", "newPackage": nil, "oldFiles": []*descriptorpb.FileDescriptorProto{op.a}, "newFiles": nil})
	if err != nil || !ok {
		t.Fatalf("absent package side: %v %v", ok, err)
	}
}

// The set indexes every file reachable and every declaration once,
// files by path.
func TestSetIndex(t *testing.T) {
	set := compileSet(t, fixture)
	if set.File("google/protobuf/descriptor.proto") == nil || set.File("b/b.proto") == nil || set.File("x") != nil {
		t.Fatal("files")
	}
	if len(set.byMsg) != set.Size() || set.Size() < 5 {
		t.Fatalf("size %d", set.Size())
	}
	for msg, en := range set.byMsg {
		if en.msg != msg {
			t.Fatalf("%s indexed under another proto", en.desc.FullName())
		}
	}
}

// Every declaration of the compiled set, dependencies included, is
// found by name, a map-entry message excepted; a file of the set by
// path; and the checked files
// bound as `files` are exactly the ones handed in, never the
// dependencies (REQ-env1-lookup-scope).
func TestLookupScope(t *testing.T) {
	env, set := lintEnv(t)
	p := fixtureProtos(set)
	var names []string
	var paths []string
	var entries []string
	for name, en := range set.byName {
		if isMapEntry(en.desc) {
			entries = append(entries, string(name))
			continue
		}
		names = append(names, string(name))
	}
	sort.Strings(entries)
	for path := range set.byPath {
		paths = append(paths, path)
	}
	sort.Strings(names)
	sort.Strings(paths)
	checked := []*descriptorpb.FileDescriptorProto{p.a, p.y}
	rapid.Check(t, func(t *rapid.T) {
		name := rapid.SampledFrom(names).Draw(t, "name")
		dotted := rapid.Bool().Draw(t, "dotted")
		expr := "fullName(resolve('" + name + "')) == '" + name + "'"
		if dotted {
			expr = "fullName(resolve('." + name + "')) == '" + name + "'"
		}
		holds(t, env, check.TargetSet, expr, map[string]any{"files": checked})
		// A map-entry message is the compiler's: named, it resolves to
		// nothing.
		entry := rapid.SampledFrom(entries).Draw(t, "entry")
		holds(t, env, check.TargetSet, "resolve('"+entry+"') == null", map[string]any{"files": checked})
		path := rapid.SampledFrom(paths).Draw(t, "path")
		holds(t, env, check.TargetSet, "fileByName('"+path+"').name == '"+path+"' && files.map(f, f.name) == ['a/a.proto', 'a/y.proto'] && (fileByName('"+path+"') in files) == ('"+path+"' in ['a/a.proto', 'a/y.proto'])", map[string]any{"files": checked})
	})
}

// A library call is charged at least the size of its result and of
// the collection it searches: over any subset of the files in any
// order, the declarations walk costs at least the files given, their
// declarations and imports, and the declarations returned; a walk
// under a message at least the declarations beneath it; a lookup by
// name at least the schema (REQ-rules-bounded).
func TestChargedBySize(t *testing.T) {
	env, set := lintEnv(t)
	all := make([]*descriptorpb.FileDescriptorProto, 0, len(set.files))
	for _, f := range set.files {
		all = append(all, f.proto)
	}
	cost := func(t *rapid.T, expr string, files []*descriptorpb.FileDescriptorProto) uint64 {
		prg, err := env.Compile(rules.Rule{ID: "C", Kind: check.KindLint, Target: check.TargetSet, CEL: expr})
		if err != nil {
			t.Fatal(err)
		}
		_, details, err := prg.eval(map[string]any{"files": files})
		if err != nil {
			t.Fatal(err)
		}
		return *details.ActualCost()
	}
	rapid.Check(t, func(t *rapid.T) {
		files := []*descriptorpb.FileDescriptorProto{}
		for _, f := range rapid.Permutation(all).Draw(t, "order") {
			if rapid.Bool().Draw(t, "in") {
				files = append(files, f)
			}
		}
		count, decls := 0, 0
		for _, f := range files {
			fd := set.byMsg[f].file.fd
			count += len(set.byMsg[f].under(kindMessage))
			decls += inFile(fd) + fd.Imports().Len()
		}
		if got, want := cost(t, "messages(files).size() >= 0", files), uint64(len(files)+decls+count); got < want {
			t.Fatalf("messages over %d files, %d declarations and imports, %d messages, charged %d < %d", len(files), decls, count, got, want)
		}
		if got, want := cost(t, "files.all(f, references(f).size() >= 0)", files), uint64(decls); got < want {
			t.Fatalf("references over %d declarations charged %d < %d", decls, got, want)
		}
		// A message is charged what lies beneath it.
		var msgs []*entry
		for _, en := range set.byMsg {
			if _, ok := en.desc.(protoreflect.MessageDescriptor); ok {
				msgs = append(msgs, en)
			}
		}
		sort.Slice(msgs, func(i, j int) bool { return msgs[i].desc.FullName() < msgs[j].desc.FullName() })
		m := rapid.SampledFrom(msgs).Draw(t, "message")
		prg, err := env.Compile(rules.Rule{ID: "M", Kind: check.KindLint, Target: check.TargetMessage, CEL: "enums(message).size() >= 0"})
		if err != nil {
			t.Fatal(err)
		}
		_, details, err := prg.eval(map[string]any{"message": m.msg, "file": m.file.proto})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := *details.ActualCost(), uint64(beneath(m.desc.(protoreflect.MessageDescriptor))); got < want {
			t.Fatalf("enums under %s (%d beneath) charged %d < %d", m.desc.FullName(), want, got, want)
		}
		if got, want := cost(t, "resolve('a.Outer') != null", files), uint64(set.Size()); got < want {
			t.Fatalf("resolve charged %d < schema size %d", got, want)
		}
	})
}

// inFile counts a file's declarations, itself included, from the
// descriptor: the oracle for what a walk over it costs.
func inFile(fd protoreflect.FileDescriptor) int {
	n := 1 + fd.Extensions().Len()
	for i, ms := 0, fd.Messages(); i < ms.Len(); i++ {
		n += beneath(ms.Get(i))
	}
	for i, es := 0, fd.Enums(); i < es.Len(); i++ {
		n += 1 + es.Get(i).Values().Len()
	}
	for i, ss := 0, fd.Services(); i < ss.Len(); i++ {
		n += 1 + ss.Get(i).Methods().Len()
	}
	return n
}

// beneath counts a message's declarations, itself included, from the
// descriptor: the oracle for what a walk under it costs.
func beneath(md protoreflect.MessageDescriptor) int {
	n := 1 + md.Fields().Len() + md.Oneofs().Len() + md.Extensions().Len()
	for i, es := 0, md.Enums(); i < es.Len(); i++ {
		n += 1 + es.Get(i).Values().Len()
	}
	for i, ms := 0, md.Messages(); i < ms.Len(); i++ {
		n += beneath(ms.Get(i))
	}
	return n
}

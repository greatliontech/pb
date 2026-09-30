// Package env1 is CEL environment 1 (check-rules.md §CEL environment
// 1): the bindings a rule sees and the library it calls, over a
// compiled schema, bounded by construction.
//
// A rule sees its entities as the standard descriptor protos, so
// every descriptor field is reachable with its proto name
// (`field.type_name`, `file.message_type`); the library reaches
// behind a proto to the declaration it came from — its parent, its
// file, its comments, its resolved features — through the identity
// the Set keeps: every descriptor proto the Set builds is indexed by
// pointer, and a proto handed to the library that the Set did not
// build is refused as no declaration of the schema, never mistaken
// for one. cel-go hands a message's nested protos out by the same
// pointers it was given, so identity survives every traversal a
// rule can write.
//
// The environment holds one Set for a lint run and two for a
// breaking run, the old side's and the new side's: a function on an
// entity finds the entity's own side; a lookup by name searches the
// new side. Nothing in the environment reads a file, a clock or the
// network: the library is pure, the standard library is CEL's, and a
// program runs under a cost limit sized to the schema (REQ-rules-
// bounded) — a rule from any source is safe to evaluate.
package env1

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"context"
	"sync"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/linker"
	"github.com/bufbuild/protocompile/wellknownimports"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/check/rules"
)

// Version is the environment's number, the one a rule file's celEnv
// names to target it.
const Version = 1

// ErrCompile is wrapped when a rule's expression does not compile
// under the environment: a parse or type error, or a result that is
// not a boolean.
var ErrCompile = errors.New("rule does not compile")

// ErrEval is wrapped when a rule's evaluation fails: a runtime error,
// a foreign descriptor, or the cost limit exceeded.
var ErrEval = errors.New("rule evaluation failed")

// Set is a compiled schema indexed for the environment: every file
// the given files reach through imports, each as its standard
// descriptor proto, every declaration indexed by the proto it is
// spelled as and by its fully qualified name.
type Set struct {
	files  []*fileEntry
	byPath map[string]*fileEntry
	byName map[protoreflect.FullName]*entry
	byMsg  map[proto.Message]*entry
	// The extensions of google.protobuf.FeatureSet the set declares —
	// the language features — by fully qualified name.
	featureExts map[protoreflect.FullName]protoreflect.ExtensionType
	// The runtime family the set's option values were decoded into
	// (New): the set's own where it is a new side, built once and kept
	// so every environment over the set reads one family; a set given
	// as an old side is decoded into the new side's, and serves that
	// new side alone.
	rebuilt []protoreflect.FileDescriptor
	family  *familyTypeResolver
	decoded *familyTypeResolver // the family the option values were last decoded into
}

// entry is one declaration: the descriptor the compiler linked, the
// proto the rule sees, the file it lies in, and what a walk under it
// costs — the declarations beneath a message, itself included.
type entry struct {
	desc  protoreflect.Descriptor
	msg   proto.Message
	file  *fileEntry
	set   *Set
	decls int
}

// isFile reports whether the entry is its file's own.
func (en *entry) isFile() bool { return en == &en.file.entry }

type fileEntry struct {
	entry
	fd    protoreflect.FileDescriptor
	proto *descriptorpb.FileDescriptorProto
	decls int // declarations in the file, the file included: what a walk over it costs
}

// entryOf is the entry of a linked descriptor of the set: a file by
// its path, any other declaration by its full name — every
// declaration, the map-entry messages included, since a rule reaches
// their fields by traversal.
func (s *Set) entryOf(d protoreflect.Descriptor) *entry {
	if fd, ok := d.(protoreflect.FileDescriptor); ok {
		return &s.byPath[fd.Path()].entry
	}
	return s.byName[d.FullName()]
}

// NewSet indexes the compiled files and every file they import,
// transitively.
func NewSet(files linker.Files) *Set {
	s := &Set{
		byPath: map[string]*fileEntry{},
		byName: map[protoreflect.FullName]*entry{},
		byMsg:  map[proto.Message]*entry{},
	}
	for _, f := range files {
		s.addFile(f)
	}
	s.featureExts = map[protoreflect.FullName]protoreflect.ExtensionType{}
	for _, f := range s.files {
		for _, xt := range featureExtensionsOf(f.fd) {
			s.featureExts[xt.TypeDescriptor().FullName()] = xt
		}
	}
	return s
}

// featureSetName is the message whose extensions are the language
// features.
const featureSetName protoreflect.FullName = "google.protobuf.FeatureSet"

// standardFeatureFiles are the language feature files protoc ships:
// their features apply to every schema at the edition's defaults
// whether or not a file imports them, so the environment knows them
// without an import.
var standardFeatureFiles = []string{"google/protobuf/java_features.proto", "google/protobuf/cpp_features.proto", "google/protobuf/go_features.proto"}

var (
	standardOnce  sync.Once
	standardFiles []protoreflect.FileDescriptor
	standardErr   error
)

// standardFeatures is the standard language feature files, each in
// the Go runtime's descriptor family: the runtime's own where it
// registers one, the compiler's embedded source compiled once —
// from the embedded copy alone, no path of the host consulted — and
// rebuilt over the runtime's descriptor.proto otherwise.
func standardFeatures() ([]protoreflect.FileDescriptor, error) {
	standardOnce.Do(func() {
		var compile []string
		for _, p := range standardFeatureFiles {
			if fd, err := protoregistry.GlobalFiles.FindFileByPath(p); err == nil {
				standardFiles = append(standardFiles, fd)
				continue
			}
			compile = append(compile, p)
		}
		embedded := wellknownimports.WithStandardImports(protocompile.ResolverFunc(func(string) (protocompile.SearchResult, error) {
			return protocompile.SearchResult{}, protoregistry.NotFound
		}))
		files, err := (&protocompile.Compiler{Resolver: embedded}).Compile(context.Background(), compile...)
		if err != nil {
			standardErr = fmt.Errorf("the standard language features: %w", err)
			return
		}
		for _, f := range files {
			fd, err := runtimeFamily(f, &protoregistry.Files{})
			if err != nil {
				standardErr = fmt.Errorf("the standard language features: %w", err)
				return
			}
			standardFiles = append(standardFiles, fd)
		}
	})
	return standardFiles, standardErr
}

// runtimeFamily is the file rebuilt over the Go runtime's descriptors
// — its descriptor.proto the runtime's, so an extension it declares
// extends the message a concrete options or feature-set value is an
// instance of — every other import served from the rebuilt files
// given; a file the runtime registers is taken as it is, so a
// well-known type a rule names is the runtime's declaration where
// the entity protos it walks are the compiler's, one and the same
// while the two modules ship the same descriptors.
func runtimeFamily(fd protoreflect.FileDescriptor, rebuilt *protoregistry.Files) (protoreflect.FileDescriptor, error) {
	if g, err := protoregistry.GlobalFiles.FindFileByPath(fd.Path()); err == nil {
		return g, nil
	}
	return protodesc.NewFile(protodesc.ToFileDescriptorProto(fd), familyResolver{rebuilt})
}

// runtimeFamilyFiles rebuilds every file of the set over the runtime's
// descriptors, imports before importers, so each file's imports are
// the rebuilt ones.
func runtimeFamilyFiles(s *Set) ([]protoreflect.FileDescriptor, error) {
	rebuilt := &protoregistry.Files{}
	var out []protoreflect.FileDescriptor
	done := map[*fileEntry]bool{}
	var visit func(f *fileEntry) error
	visit = func(f *fileEntry) error {
		if done[f] {
			return nil
		}
		done[f] = true
		imports := f.fd.Imports()
		for i := 0; i < imports.Len(); i++ {
			if dep := s.byPath[imports.Get(i).Path()]; dep != nil {
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		rf, err := runtimeFamily(f.fd, rebuilt)
		if err != nil {
			return fmt.Errorf("%s: %w", f.fd.Path(), err)
		}
		if _, err := rebuilt.FindFileByPath(rf.Path()); err != nil {
			if err := rebuilt.RegisterFile(rf); err != nil {
				return fmt.Errorf("%s: %w", f.fd.Path(), err)
			}
		}
		out = append(out, rf)
		return nil
	}
	for _, f := range s.files {
		if err := visit(f); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// familyTypes is every extension the rebuilt files declare, top-level
// and nested to any depth, as extension types over the family's
// descriptors — the runtime's own where it registers the file — the
// resolver option values are decoded with. The schema's own
// declarations alone answer: an extension the running binary
// registers at the same number under another name is no declaration
// of the schema, and decoding through it would read the schema's
// value as the binary's. Registration is first-wins in the files'
// order — the schema's files before the standard feature files — so
// a schema declaring a standard feature at its number under its own
// path stands, the standard file's declaration behind it.
func familyTypes(rebuilt []protoreflect.FileDescriptor) *familyTypeResolver {
	r := &familyTypeResolver{own: &protoregistry.Types{}}
	for _, fd := range rebuilt {
		for _, xd := range extensionsOf(fd) {
			extendee := xd.ContainingMessage().FullName()
			if _, err := r.own.FindExtensionByName(xd.FullName()); err == nil {
				continue
			}
			if _, err := r.own.FindExtensionByNumber(extendee, xd.Number()); err == nil {
				continue
			}
			// Both lookups refused what would conflict, so the
			// registration cannot fail.
			r.own.RegisterExtension(extensionType(xd))
		}
	}
	return r
}

// extensionsOf is every extension a file declares, top-level and
// within its messages to any depth, in declaration order.
func extensionsOf(fd protoreflect.FileDescriptor) []protoreflect.ExtensionDescriptor {
	var out []protoreflect.ExtensionDescriptor
	collect := func(exts protoreflect.ExtensionDescriptors) {
		for i := 0; i < exts.Len(); i++ {
			out = append(out, exts.Get(i))
		}
	}
	var walk func(msgs protoreflect.MessageDescriptors)
	walk = func(msgs protoreflect.MessageDescriptors) {
		for i := 0; i < msgs.Len(); i++ {
			collect(msgs.Get(i).Extensions())
			walk(msgs.Get(i).Messages())
		}
	}
	collect(fd.Extensions())
	walk(fd.Messages())
	return out
}

// familyTypeResolver resolves extensions for decoding from the
// family's own declarations alone; messages, which decoding of an
// Any needs, from the runtime's registry.
type familyTypeResolver struct{ own *protoregistry.Types }

func (r *familyTypeResolver) FindExtensionByName(field protoreflect.FullName) (protoreflect.ExtensionType, error) {
	return r.own.FindExtensionByName(field)
}

func (r *familyTypeResolver) FindExtensionByNumber(message protoreflect.FullName, field protoreflect.FieldNumber) (protoreflect.ExtensionType, error) {
	return r.own.FindExtensionByNumber(message, field)
}

func (r *familyTypeResolver) FindMessageByName(message protoreflect.FullName) (protoreflect.MessageType, error) {
	return protoregistry.GlobalTypes.FindMessageByName(message)
}

func (r *familyTypeResolver) FindMessageByURL(url string) (protoreflect.MessageType, error) {
	return protoregistry.GlobalTypes.FindMessageByURL(url)
}

// redecodeOptions replaces every options message beneath m — a file's,
// a declaration's, an extension range's — with the same bytes decoded
// through the family's types, walking every message and list of
// messages the descriptor proto holds; an options message the family
// cannot decode — an old value the new schema's declarations refuse —
// is left as the compiler holds it.
func redecodeOptions(m protoreflect.Message, family *familyTypeResolver) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Kind() != protoreflect.MessageKind || fd.IsMap() {
			return true
		}
		if fd.Name() == "options" {
			if fresh := redecode(v.Message(), family); fresh != nil {
				m.Set(fd, protoreflect.ValueOfMessage(fresh))
			}
			return true
		}
		if fd.IsList() {
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				redecodeOptions(l.Get(i).Message(), family)
			}
			return true
		}
		redecodeOptions(v.Message(), family)
		return true
	})
}

// redecode is the message's bytes decoded again through the family's
// types into a fresh message of the same type — a required field the
// bytes lack no refusal, the value being a declaration's, not a wire
// message — or nil where the bytes do not decode under the family.
func redecode(m protoreflect.Message, family *familyTypeResolver) protoreflect.Message {
	b, err := proto.MarshalOptions{AllowPartial: true}.Marshal(m.Interface())
	if err != nil {
		return nil
	}
	fresh := m.New()
	if err := (proto.UnmarshalOptions{AllowPartial: true, Resolver: family}).Unmarshal(b, fresh.Interface()); err != nil {
		return nil
	}
	return fresh
}

// familyResolver serves the runtime's files first and the rebuilt
// ones after them.
type familyResolver struct{ rebuilt *protoregistry.Files }

func (r familyResolver) FindFileByPath(path string) (protoreflect.FileDescriptor, error) {
	if fd, err := protoregistry.GlobalFiles.FindFileByPath(path); err == nil {
		return fd, nil
	}
	return r.rebuilt.FindFileByPath(path)
}

func (r familyResolver) FindDescriptorByName(name protoreflect.FullName) (protoreflect.Descriptor, error) {
	if d, err := protoregistry.GlobalFiles.FindDescriptorByName(name); err == nil {
		return d, nil
	}
	return r.rebuilt.FindDescriptorByName(name)
}

// featureExtensionsOf lists a file's extensions of FeatureSet,
// declared at the top level or within a message.
func featureExtensionsOf(fd protoreflect.FileDescriptor) []protoreflect.ExtensionType {
	var out []protoreflect.ExtensionType
	for _, xd := range extensionsOf(fd) {
		if xd.ContainingMessage().FullName() == featureSetName {
			out = append(out, extensionType(xd))
		}
	}
	return out
}

// extensionType is the extension's type: the compiler's own where it
// carries one, a dynamic one over the descriptor otherwise.
func extensionType(xd protoreflect.ExtensionDescriptor) protoreflect.ExtensionType {
	if xtd, ok := xd.(protoreflect.ExtensionTypeDescriptor); ok {
		return xtd.Type()
	}
	return dynamicpb.NewExtensionType(xd)
}

// Size is the number of declarations the set indexes, files
// included: the unit the cost limit is sized in.
func (s *Set) Size() int { return len(s.byMsg) }

// File is the descriptor proto of the file at path, or nil.
func (s *Set) File(path string) *descriptorpb.FileDescriptorProto {
	if f := s.byPath[path]; f != nil {
		return f.proto
	}
	return nil
}

func (s *Set) addFile(fd protoreflect.FileDescriptor) *fileEntry {
	if f := s.byPath[fd.Path()]; f != nil {
		return f
	}
	// The compiler's own proto of a file it parsed keeps what the
	// descriptor view forgets — a declared proto2 syntax, a weak
	// import; a file known by descriptor alone is rebuilt from it.
	// A copy: the set's option values are re-decoded into the
	// environment's family (New), and the compiler's own proto stays
	// as it parsed it.
	var fdp *descriptorpb.FileDescriptorProto
	if r, ok := fd.(linker.Result); ok {
		fdp = proto.Clone(r.FileDescriptorProto()).(*descriptorpb.FileDescriptorProto)
	} else {
		fdp = protodesc.ToFileDescriptorProto(fd)
	}
	f := &fileEntry{fd: fd, proto: fdp}
	f.entry = entry{desc: fd, msg: fdp, file: f, set: s}
	s.byPath[fd.Path()] = f
	s.byMsg[fdp] = &f.entry
	f.decls = 1
	s.files = append(s.files, f)
	imports := fd.Imports()
	for i := 0; i < imports.Len(); i++ {
		s.addFile(imports.Get(i).FileDescriptor)
	}
	s.indexMessages(f, fd.Messages(), fdp.MessageType)
	s.indexEnums(f, fd.Enums(), fdp.EnumType)
	s.indexFields(f, fd.Extensions(), fdp.Extension)
	services := fd.Services()
	for i := 0; i < services.Len(); i++ {
		sd, sp := services.Get(i), fdp.Service[i]
		s.add(f, sd, sp)
		methods := sd.Methods()
		for j := 0; j < methods.Len(); j++ {
			s.add(f, methods.Get(j), sp.Method[j])
		}
	}
	return f
}

// indexMessages indexes messages and everything beneath them,
// recording under each message what lies beneath it.
func (s *Set) indexMessages(f *fileEntry, msgs protoreflect.MessageDescriptors, protos []*descriptorpb.DescriptorProto) {
	for i := 0; i < msgs.Len(); i++ {
		md, mp := msgs.Get(i), protos[i]
		before := f.decls
		me := s.add(f, md, mp)
		s.indexFields(f, md.Fields(), mp.Field)
		oneofs := md.Oneofs()
		for j := 0; j < oneofs.Len(); j++ {
			s.add(f, oneofs.Get(j), mp.OneofDecl[j])
		}
		s.indexEnums(f, md.Enums(), mp.EnumType)
		s.indexFields(f, md.Extensions(), mp.Extension)
		s.indexMessages(f, md.Messages(), mp.NestedType)
		me.decls = f.decls - before
	}
}

// fieldList is what fields and extensions share: a counted list of
// field descriptors.
type fieldList interface {
	Len() int
	Get(int) protoreflect.FieldDescriptor
}

func (s *Set) indexFields(f *fileEntry, fields fieldList, protos []*descriptorpb.FieldDescriptorProto) {
	for i := 0; i < fields.Len(); i++ {
		s.add(f, fields.Get(i), protos[i])
	}
}

func (s *Set) indexEnums(f *fileEntry, enums protoreflect.EnumDescriptors, protos []*descriptorpb.EnumDescriptorProto) {
	for i := 0; i < enums.Len(); i++ {
		ed, ep := enums.Get(i), protos[i]
		s.add(f, ed, ep)
		values := ed.Values()
		for j := 0; j < values.Len(); j++ {
			s.add(f, values.Get(j), ep.Value[j])
		}
	}
}

// add indexes one declaration by its proto and by its name.
func (s *Set) add(f *fileEntry, d protoreflect.Descriptor, msg proto.Message) *entry {
	e := &entry{desc: d, msg: msg, file: f, set: s, decls: 1}
	s.byMsg[msg] = e
	s.byName[d.FullName()] = e
	f.decls++
	return e
}

// Env is environment 1 over a schema: one set for a lint run, the old
// and new sides for a breaking run.
type Env struct {
	new, old *Set
	base     *cel.Env
	adapter  types.Adapter
	limit    uint64
	// The library's functions by name: the ones the cost tracker
	// charges by size.
	charged map[string]bool
	// The language features a rule can name: every extension of
	// FeatureSet the new side declares, and the standard ones — the
	// files protoc ships — where the schema holds no file of that
	// path; each in the runtime's descriptor family, the registry's.
	featureExts []protoreflect.ExtensionType
	// The environment extended with each target's bindings, for the
	// one kind the environment compiles.
	byTarget map[check.Target]*cel.Env
	// The rule files' functions compiled under the environment
	// (REQ-rules-functions): each scope's declarations, each function
	// once, the functions by their overload id for the cost tracker,
	// and a target's environment extended with a scope's declarations.
	scopes     map[*rules.Scope]*compiledScope
	functions  map[functionKey]*userFunction
	byOverload map[string]*userFunction
	byScope    map[scopeKey]*cel.Env
	// The environment's macro names, which a function may not take.
	macros map[string]bool
}

// scopeKey names a target's environment under one scope.
type scopeKey struct {
	target check.Target
	scope  *rules.Scope
}

// New is the environment over the new side, and the old side where a
// breaking run compares against one (nil for a lint run).
func New(newSide, oldSide *Set) (*Env, error) {
	e := &Env{new: newSide, old: oldSide, byTarget: map[check.Target]*cel.Env{}, scopes: map[*rules.Scope]*compiledScope{}, functions: map[functionKey]*userFunction{}, byOverload: map[string]*userFunction{}, byScope: map[scopeKey]*cel.Env{}}
	size := newSide.Size()
	if oldSide != nil {
		size += oldSide.Size()
	}
	e.limit = costLimit(size)
	opts := []cel.EnvOption{
		cel.Types(&descriptorpb.FileDescriptorProto{}),
		// The extension libraries at the versions REQ-env1-library
		// names: each at the last version the library defines, so a
		// later cel-go's unversioned behavior never reaches a rule —
		// lists at 3 keeps its versioned cost estimators for flatten,
		// distinct and sort — and the libraries with no versioned
		// behavior at their first.
		ext.Strings(ext.StringsVersion(5)),
		ext.Lists(ext.ListsVersion(3)),
		ext.Math(ext.MathVersion(3)),
		ext.Encoders(ext.EncodersVersion(1)),
		ext.Bindings(ext.BindingsVersion(1)),
		ext.Sets(ext.SetsVersion(0)),
		ext.TwoVarComprehensions(ext.TwoVarComprehensionsVersion(0)),
		ext.Protos(ext.ProtosVersion(0)),
		// Optional values, the regex library's prerequisite.
		cel.OptionalTypes(),
		ext.Regex(ext.RegexVersion(0)),
	}
	// The checked schema's own types and extensions, so a rule can
	// name an enum's value, a custom option or a language feature's
	// extension: every file of the new side rebuilt over the runtime's
	// descriptors, since the registry reads an extension only from a
	// value of the message the registered extension extends, and the
	// concrete options and feature sets a rule sees are the runtime's;
	// then the standard language features, where the schema holds no
	// file of that path.
	if newSide.rebuilt == nil {
		rebuilt, err := runtimeFamilyFiles(newSide)
		if err != nil {
			return nil, fmt.Errorf("the schema's types: %w", err)
		}
		standard, err := standardFeatures()
		if err != nil {
			return nil, err
		}
		// A standard feature file the schema holds at its path, or whose
		// feature the schema declares itself under another path — a
		// vendored copy, which the compiler holds to the standard name
		// and number — is the schema's to declare.
		declared := map[protoreflect.FullName]bool{}
		for _, fd := range rebuilt {
			for _, xd := range extensionsOf(fd) {
				declared[xd.FullName()] = true
			}
		}
		for _, fd := range standard {
			if newSide.byPath[fd.Path()] != nil {
				continue
			}
			own := false
			for _, xd := range extensionsOf(fd) {
				own = own || declared[xd.FullName()]
			}
			if !own {
				rebuilt = append(rebuilt, fd)
			}
		}
		newSide.rebuilt, newSide.family = rebuilt, familyTypes(rebuilt)
	}
	for _, fd := range newSide.rebuilt {
		opts = append(opts, cel.TypeDescs(fd))
		e.featureExts = append(e.featureExts, featureExtensionsOf(fd)...)
	}
	// Option values re-decoded into that family: the compiler stores a
	// message-valued custom option as a dynamic message over its own
	// descriptor, whose fields a rule reads by name but whose
	// extensions the registry, answering for the rebuilt family alone,
	// cannot reach; decoded again through the family's extension
	// types, a value's extensions are the family's own, on both sides
	// — an extension the old side alone declared stays unread, as the
	// schema no longer has it, and a value the family's declarations
	// cannot decode keeps the compiler's own, its extensions within
	// unreachable as before (REQ-env1-library).
	for _, side := range []*Set{newSide, oldSide} {
		if side == nil || side.decoded == newSide.family {
			continue
		}
		for _, f := range side.files {
			redecodeOptions(f.proto.ProtoReflect(), newSide.family)
		}
		side.decoded = newSide.family
	}
	e.charged = map[string]bool{}
	for _, f := range e.library() {
		opts = append(opts, f.opt)
		e.charged[f.name] = true
	}
	base, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, err
	}
	e.base = base
	e.adapter = base.CELTypeAdapter()
	e.macros = map[string]bool{}
	for _, m := range base.Macros() {
		e.macros[m.Function()] = true
	}
	return e, nil
}

// Sides is the environment's new side and, for a breaking
// environment, its old side (nil for a lint environment).
func (e *Env) Sides() (newSide, oldSide *Set) { return e.new, e.old }

// Binding names (REQ-env1-bindings): a package rule's name is `pkg`,
// `package` being a reserved word of CEL.
const (
	BindFile     = "file"
	BindFiles    = "files"
	BindPackage  = "pkg"
	BindOld      = "old"
	BindNew      = "new"
	BindOldFile  = "oldFile"
	BindNewFile  = "newFile"
	BindOldFiles = "oldFiles"
	BindNewFiles = "newFiles"
	BindOldPkg   = "oldPackage"
	BindNewPkg   = "newPackage"
)

// EntityBinding is the name a lint rule's entity is bound under: its
// target's name, `enum-value` as `enumValue` since a binding is an
// identifier.
func EntityBinding(t check.Target) string {
	if t == check.TargetEnumValue {
		return "enumValue"
	}
	return string(t)
}

var (
	fileType  = cel.ObjectType("google.protobuf.FileDescriptorProto")
	filesType = cel.ListType(fileType)
)

// descriptorType is the descriptor proto type an entity target binds.
func descriptorType(t check.Target) *cel.Type { return targets[t].typ }

// bindings declares the variables a rule of the kind and target sees
// (REQ-env1-bindings).
func bindings(kind check.Kind, t check.Target) []cel.EnvOption {
	if kind == check.KindLint {
		switch t {
		case check.TargetPackage:
			return []cel.EnvOption{cel.Variable(BindPackage, cel.StringType), cel.Variable(BindFiles, filesType)}
		case check.TargetSet:
			return []cel.EnvOption{cel.Variable(BindFiles, filesType)}
		case check.TargetFile:
			return []cel.EnvOption{cel.Variable(BindFile, fileType)}
		}
		return []cel.EnvOption{cel.Variable(EntityBinding(t), descriptorType(t)), cel.Variable(BindFile, fileType)}
	}
	nullable := types.NewNullableType
	switch t {
	case check.TargetPackage:
		return []cel.EnvOption{
			cel.Variable(BindOldPkg, nullable(cel.StringType)), cel.Variable(BindNewPkg, nullable(cel.StringType)),
			cel.Variable(BindOldFiles, nullable(filesType)), cel.Variable(BindNewFiles, nullable(filesType)),
		}
	case check.TargetSet:
		return []cel.EnvOption{cel.Variable(BindOldFiles, filesType), cel.Variable(BindNewFiles, filesType)}
	case check.TargetFile:
		// A file's file is itself: the pair alone.
		return []cel.EnvOption{cel.Variable(BindOld, nullable(fileType)), cel.Variable(BindNew, nullable(fileType))}
	}
	dt := descriptorType(t)
	return []cel.EnvOption{
		cel.Variable(BindOld, nullable(dt)), cel.Variable(BindNew, nullable(dt)),
		cel.Variable(BindOldFile, nullable(fileType)), cel.Variable(BindNewFile, nullable(fileType)),
	}
}

// Program is one rule compiled under the environment for its kind and
// target, ready to evaluate over bindings.
type Program struct {
	rule rules.Rule
	prg  cel.Program
}

// Compile compiles a rule of the environment's version: its expression
// type-checked against the bindings its kind and target declare and
// the library, and refused unless it yields a boolean (REQ-rules-
// verdict). A breaking environment compiles breaking rules and a lint
// environment lint rules; the other kind is refused.
func (e *Env) Compile(r rules.Rule) (*Program, error) {
	wantKind := check.KindLint
	if e.old != nil {
		wantKind = check.KindBreaking
	}
	if r.Kind != wantKind {
		return nil, fmt.Errorf("%w: %s is a %s rule under a %s environment", ErrCompile, r.Name(), r.Kind, wantKind)
	}
	env, err := e.forTarget(r.Kind, r.Target)
	if err != nil {
		return nil, err
	}
	// The rule's file's functions, its own and its imports'
	// (REQ-rules-functions), compiled before the rule.
	if r.Scope != nil {
		env, err = e.forScope(env, r.Target, r.Scope)
		if err != nil {
			if errors.Is(err, ErrCompile) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %s: %v", ErrCompile, r.Name(), err)
		}
	}
	ast, iss := env.Compile(r.CEL)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCompile, r.Name(), iss.Err())
	}
	if !ast.OutputType().IsExactType(cel.BoolType) {
		return nil, fmt.Errorf("%w: %s: the expression yields %s, not bool", ErrCompile, r.Name(), ast.OutputType())
	}
	prg, err := env.Program(ast, cel.CostLimit(e.limit), cel.CostTracking(costs{e}), cel.EvalOptions(cel.OptOptimize))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCompile, r.Name(), err)
	}
	return &Program{rule: r, prg: prg}, nil
}

// forScope is the target's environment extended with a scope's
// declarations, once per pair.
func (e *Env) forScope(target *cel.Env, t check.Target, sc *rules.Scope) (*cel.Env, error) {
	key := scopeKey{t, sc}
	if env := e.byScope[key]; env != nil {
		return env, nil
	}
	opts, err := e.scope(sc)
	if err != nil {
		return nil, err
	}
	env, err := target.Extend(opts...)
	if err != nil {
		return nil, err
	}
	e.byScope[key] = env
	return env, nil
}

func (e *Env) forTarget(kind check.Kind, t check.Target) (*cel.Env, error) {
	if env := e.byTarget[t]; env != nil {
		return env, nil
	}
	env, err := e.base.Extend(bindings(kind, t)...)
	if err != nil {
		return nil, err
	}
	e.byTarget[t] = env
	return env, nil
}

// Rule is the rule the program was compiled from.
func (p *Program) Rule() rules.Rule { return p.rule }

// Eval evaluates the program over the bindings, a value per declared
// name — a descriptor proto the environment's sets built, a list of
// such, a string, or nil for an absent side — yielding the verdict:
// true is compliance, false one finding (REQ-rules-verdict). A
// runtime error, a foreign descriptor, and the cost limit exceeded
// are evaluation failures, never verdicts.
func (p *Program) Eval(vars map[string]any) (bool, error) {
	v, _, err := p.eval(vars)
	return v, err
}

func (p *Program) eval(vars map[string]any) (bool, *cel.EvalDetails, error) {
	out, details, err := p.prg.Eval(vars)
	if err != nil {
		// The interpreter recovers a panic into an "internal error":
		// a select the contract puts beyond reach, or a fault of the
		// library's own; the rule's failure names the environment as
		// its cause and keeps the recovered text after the name, so a
		// fault stays diagnosable (REQ-rules-eval).
		if strings.HasPrefix(err.Error(), "internal error:") {
			return false, details, fmt.Errorf("%w: %s: the expression reached a value the environment cannot answer (%v)", ErrEval, p.rule.Name(), err)
		}
		return false, details, fmt.Errorf("%w: %s: %v", ErrEval, p.rule.Name(), err)
	}
	// Compile admitted a boolean expression alone, so a value that is
	// not an error is a bool.
	return bool(out.(types.Bool)), details, nil
}

// lookup finds the declaration a descriptor proto the rule holds
// came from, on whichever side built it.
func (e *Env) lookup(msg proto.Message) (*entry, error) {
	if en := e.new.byMsg[msg]; en != nil {
		return en, nil
	}
	if e.old != nil {
		if en := e.old.byMsg[msg]; en != nil {
			return en, nil
		}
	}
	return nil, fmt.Errorf("%s is no declaration of the compiled schema", msg.ProtoReflect().Descriptor().FullName())
}

// value is a descriptor proto, or nil, as a CEL value.
func (e *Env) value(msg proto.Message) ref.Val {
	if msg == nil {
		return types.NullValue
	}
	return e.adapter.NativeToValue(msg)
}

// entityArg reads a library argument naming a descriptor: null is
// null, a proto the sets built is its entry, anything else an error.
func (e *Env) entityArg(v ref.Val) (*entry, ref.Val) {
	if v == types.NullValue {
		return nil, types.NullValue
	}
	msg, ok := v.Value().(proto.Message)
	if !ok {
		return nil, types.NewErr("%s is not a descriptor", v.Type().TypeName())
	}
	en, err := e.lookup(msg)
	if err != nil {
		return nil, types.WrapErr(err)
	}
	return en, nil
}

// Binding is one member of a rule's population: the variables a rule
// of that target sees, the declarations bound — the new side's, the
// checked schema's for a lint rule, and for a breaking pair the old
// side's too, nil where absent, both nil for a package or the set —
// and the path a finding names: the entity's file, a package's first
// checked file in path order, none for the set.
type Binding struct {
	Vars map[string]any
	New  protoreflect.Descriptor
	Old  protoreflect.Descriptor
	Path string
	// Base marks a pair whose new side is absent: its finding lies in
	// the comparison base. A package pair binds no descriptor, so the
	// mark is stated rather than derived.
	Base bool
}

// Located is the declaration a finding is located at: the new side's,
// or the old side's in the base where the new side is absent; nil for
// a package or the set.
func (b Binding) Located() protoreflect.Descriptor {
	if b.New != nil {
		return b.New
	}
	return b.Old
}

// Population lists a lint target's bindings over the checked files,
// given by path, taken once each in path order (REQ-env1-population):
// every entity of the target's kind in those files and no other —
// messages nested ones included and map entries excluded, fields of
// bound messages with oneof members, oneofs without the synthetic
// ones of proto3 optional fields, enums and their values, services
// and their methods, extensions wherever declared; each package a
// file declares once with its checked files, a file declaring none
// binding none; the set once over them all. A path the set does not
// hold is an error.
func (s *Set) Population(t check.Target, checked []string) ([]Binding, error) {
	tg, ok := targets[t]
	if !ok {
		return nil, fmt.Errorf("%q is no target", t)
	}
	paths := append([]string(nil), checked...)
	sort.Strings(paths)
	paths = slices.Compact(paths)
	files := make([]*fileEntry, 0, len(paths))
	for _, p := range paths {
		f := s.byPath[p]
		if f == nil {
			return nil, fmt.Errorf("%s is not a file of the compiled schema", p)
		}
		files = append(files, f)
	}
	if tg.populate != nil {
		return tg.populate(files), nil
	}
	return populateEntities(t, files, tg.each), nil
}

// populateSet binds the set once over the files.
func populateSet(files []*fileEntry) []Binding {
	return []Binding{{Vars: map[string]any{BindFiles: fileProtos(files)}}}
}

// populatePackages binds each declared package once, its files in
// path order, at its first file.
func populatePackages(files []*fileEntry) []Binding {
	var out []Binding
	var order []string
	byPkg := map[string][]*fileEntry{}
	for _, f := range files {
		pkg := string(f.fd.Package())
		if pkg == "" {
			continue
		}
		if _, seen := byPkg[pkg]; !seen {
			order = append(order, pkg)
		}
		byPkg[pkg] = append(byPkg[pkg], f)
	}
	for _, pkg := range order {
		out = append(out, Binding{Vars: map[string]any{BindPackage: pkg, BindFiles: fileProtos(byPkg[pkg])}, Path: byPkg[pkg][0].fd.Path()})
	}
	return out
}

// populateEntities binds every entity of the target's kind in each
// file, beside the file.
func populateEntities(t check.Target, files []*fileEntry, each func(*fileEntry, func(protoreflect.Descriptor))) []Binding {
	var out []Binding
	name := EntityBinding(t)
	for _, f := range files {
		each(f, func(d protoreflect.Descriptor) {
			out = append(out, Binding{Vars: map[string]any{name: f.set.entryOf(d).msg, BindFile: f.proto}, New: d, Path: f.fd.Path()})
		})
	}
	return out
}

// sideValue is a side's binding value, nil for an absent side.
func sideValue(b *Binding, name string) any {
	if b == nil {
		return nil
	}
	return b.Vars[name]
}

// pairPackage binds a package pair: the names and the file lists.
func pairPackage(old, new *Binding) map[string]any {
	return map[string]any{
		BindOldPkg: sideValue(old, BindPackage), BindNewPkg: sideValue(new, BindPackage),
		BindOldFiles: sideValue(old, BindFiles), BindNewFiles: sideValue(new, BindFiles),
	}
}

// pairEntity binds an entity pair: the two forms of the entity and,
// for every target but file, of its file.
func pairEntity(t check.Target) func(old, new *Binding) map[string]any {
	return func(old, new *Binding) map[string]any {
		if t == check.TargetFile {
			return map[string]any{BindOld: sideValue(old, BindFile), BindNew: sideValue(new, BindFile)}
		}
		name := EntityBinding(t)
		return map[string]any{
			BindOld: sideValue(old, name), BindNew: sideValue(new, name),
			BindOldFile: sideValue(old, BindFile), BindNewFile: sideValue(new, BindFile),
		}
	}
}

// eachMessage visits every bound message of the file, nested ones
// included and map entries excluded.
func eachMessage(f *fileEntry, visit func(protoreflect.MessageDescriptor)) {
	for _, m := range f.under(kindMessage) {
		visit(f.set.byMsg[m].desc.(protoreflect.MessageDescriptor))
	}
}

func eachEnum(f *fileEntry, visit func(protoreflect.EnumDescriptor)) {
	for _, e := range f.under(kindEnum) {
		visit(f.set.byMsg[e].desc.(protoreflect.EnumDescriptor))
	}
}

func eachService(f *fileEntry, visit func(protoreflect.ServiceDescriptor)) {
	for _, s := range f.under(kindService) {
		visit(f.set.byMsg[s].desc.(protoreflect.ServiceDescriptor))
	}
}

// targets is the one table over the targets: the descriptor proto
// type a lint rule's entity binds, the walk yielding each entity of
// the kind in a file — or, for the two targets binding no entity, how
// their population is drawn — and the breaking pair's bindings from
// each side's own (nil for an entity target: the entity pair's).
var targets = map[check.Target]struct {
	typ      *cel.Type
	each     func(*fileEntry, func(protoreflect.Descriptor))
	populate func([]*fileEntry) []Binding
	pair     func(old, new *Binding) map[string]any
}{
	check.TargetSet:     {populate: populateSet},
	check.TargetPackage: {populate: populatePackages, pair: pairPackage},
	check.TargetFile: {typ: fileType, each: func(f *fileEntry, bind func(protoreflect.Descriptor)) {
		bind(f.fd)
	}},
	check.TargetMessage: {typ: cel.ObjectType("google.protobuf.DescriptorProto"), each: func(f *fileEntry, bind func(protoreflect.Descriptor)) {
		eachMessage(f, func(md protoreflect.MessageDescriptor) { bind(md) })
	}},
	check.TargetField: {typ: cel.ObjectType("google.protobuf.FieldDescriptorProto"), each: func(f *fileEntry, bind func(protoreflect.Descriptor)) {
		eachMessage(f, func(md protoreflect.MessageDescriptor) {
			for i, fs := 0, md.Fields(); i < fs.Len(); i++ {
				bind(fs.Get(i))
			}
		})
	}},
	check.TargetOneof: {typ: cel.ObjectType("google.protobuf.OneofDescriptorProto"), each: func(f *fileEntry, bind func(protoreflect.Descriptor)) {
		eachMessage(f, func(md protoreflect.MessageDescriptor) {
			for i, os := 0, md.Oneofs(); i < os.Len(); i++ {
				if !os.Get(i).IsSynthetic() {
					bind(os.Get(i))
				}
			}
		})
	}},
	check.TargetEnum: {typ: cel.ObjectType("google.protobuf.EnumDescriptorProto"), each: func(f *fileEntry, bind func(protoreflect.Descriptor)) {
		eachEnum(f, func(ed protoreflect.EnumDescriptor) { bind(ed) })
	}},
	check.TargetEnumValue: {typ: cel.ObjectType("google.protobuf.EnumValueDescriptorProto"), each: func(f *fileEntry, bind func(protoreflect.Descriptor)) {
		eachEnum(f, func(ed protoreflect.EnumDescriptor) {
			for i, vs := 0, ed.Values(); i < vs.Len(); i++ {
				bind(vs.Get(i))
			}
		})
	}},
	check.TargetService: {typ: cel.ObjectType("google.protobuf.ServiceDescriptorProto"), each: func(f *fileEntry, bind func(protoreflect.Descriptor)) {
		eachService(f, func(sd protoreflect.ServiceDescriptor) { bind(sd) })
	}},
	check.TargetMethod: {typ: cel.ObjectType("google.protobuf.MethodDescriptorProto"), each: func(f *fileEntry, bind func(protoreflect.Descriptor)) {
		eachService(f, func(sd protoreflect.ServiceDescriptor) {
			for i, ms := 0, sd.Methods(); i < ms.Len(); i++ {
				bind(ms.Get(i))
			}
		})
	}},
	check.TargetExtension: {typ: cel.ObjectType("google.protobuf.FieldDescriptorProto"), each: func(f *fileEntry, bind func(protoreflect.Descriptor)) {
		for _, x := range f.under(kindExtension) {
			bind(f.set.byMsg[x].desc)
		}
	}},
}

func init() {
	for t, row := range targets {
		if row.pair == nil {
			row.pair = pairEntity(t)
			targets[t] = row
		}
	}
}

func fileProtos(files []*fileEntry) []*descriptorpb.FileDescriptorProto {
	out := make([]*descriptorpb.FileDescriptorProto, len(files))
	for i, f := range files {
		out[i] = f.proto
	}
	return out
}

// Pairs aligns a breaking target's entities across the old and new
// sides' checked files (REQ-break-pairing): files by path; packages,
// messages, enums, services, methods and extensions by fully
// qualified name; fields and enum values by number within their
// paired parent — enum values sharing a number, aliases, by name
// among them; oneofs by name within their paired message; an entity
// on one side alone paired with an absent side; the set once over the
// two sides. Pairs come in the new side's order, then the old side's
// for entities the new side lacks.
func Pairs(t check.Target, old, new *Set, oldChecked, newChecked []string) ([]Binding, error) {
	oldSide, err := old.Population(t, oldChecked)
	if err != nil {
		return nil, err
	}
	newSide, err := new.Population(t, newChecked)
	if err != nil {
		return nil, err
	}
	if t == check.TargetSet {
		return []Binding{{Vars: map[string]any{BindOldFiles: oldSide[0].Vars[BindFiles], BindNewFiles: newSide[0].Vars[BindFiles]}}}, nil
	}
	type side struct{ old, new *Binding }
	var slots []*side
	byKey := map[string]*side{}
	oldKeys, newKeys := pairKeys(t, oldSide, newSide)
	for i, k := range newKeys {
		s := &side{new: &newSide[i]}
		slots = append(slots, s)
		if _, taken := byKey[k]; !taken {
			byKey[k] = s
		}
	}
	for i, k := range oldKeys {
		// An occupied slot is never overwritten: a counterpart joins
		// its pair, anything else stands alone.
		if s, ok := byKey[k]; ok && s.old == nil {
			s.old = &oldSide[i]
			continue
		}
		slots = append(slots, &side{old: &oldSide[i]})
	}
	out := make([]Binding, 0, len(slots))
	for _, s := range slots {
		b := Binding{Vars: targets[t].pair(s.old, s.new)}
		if s.new != nil {
			b.New, b.Path = s.new.New, s.new.Path
		}
		if s.old != nil {
			b.Old = s.old.New
			if s.new == nil {
				b.Path, b.Base = s.old.Path, true
			}
		}
		out = append(out, b)
	}
	return out, nil
}

// pairKeys is what aligns each side's entities with their
// counterparts: a file's path, a package's name, a field's number
// within its parent's full name, an enum value's number within its
// enum's — and, where either side has several values of one enum at
// one number, the name among them — a oneof's name within its
// message's, any other's full name.
func pairKeys(t check.Target, oldSide, newSide []Binding) (oldKeys, newKeys []string) {
	key := func(b Binding) string {
		d := b.Located()
		switch t {
		case check.TargetPackage:
			return b.Vars[BindPackage].(string)
		case check.TargetFile:
			return b.Path
		case check.TargetField:
			fd := d.(protoreflect.FieldDescriptor)
			return string(fd.ContainingMessage().FullName()) + "#" + strconv.Itoa(int(fd.Number()))
		case check.TargetEnumValue:
			vd := d.(protoreflect.EnumValueDescriptor)
			return string(vd.Parent().FullName()) + "#" + strconv.Itoa(int(vd.Number()))
		case check.TargetOneof:
			return string(d.Parent().FullName()) + "/" + string(d.Name())
		}
		return string(d.FullName())
	}
	keysOf := func(side []Binding) []string {
		keys := make([]string, len(side))
		for i, b := range side {
			keys[i] = key(b)
		}
		return keys
	}
	oldKeys, newKeys = keysOf(oldSide), keysOf(newSide)
	if t != check.TargetEnumValue {
		return oldKeys, newKeys
	}
	// Aliases: a number that either side holds several values at
	// pairs its values by name on both sides.
	aliased := map[string]bool{}
	for _, keys := range [][]string{oldKeys, newKeys} {
		count := map[string]int{}
		for _, k := range keys {
			count[k]++
			if count[k] > 1 {
				aliased[k] = true
			}
		}
	}
	name := func(side []Binding, keys []string) {
		for i, k := range keys {
			if aliased[k] {
				keys[i] = k + "/" + string(side[i].Located().Name())
			}
		}
	}
	name(oldSide, oldKeys)
	name(newSide, newKeys)
	return oldKeys, newKeys
}

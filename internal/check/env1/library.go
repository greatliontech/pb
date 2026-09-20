package env1

import (
	"fmt"
	"sort"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"github.com/bufbuild/protocompile/protoutil"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// libraryFunctions names pb's functions (REQ-env1-library): the ones
// the cost tracker charges by size.
var libraryFunctions = map[string]bool{
	"comments": true, "parent": true, "file": true, "fullName": true,
	"messages": true, "enums": true, "extensions": true, "services": true,
	"resolve": true, "fileByName": true, "imports": true, "visible": true,
	"references": true, "features": true, "options": true,
	"words": true, "case": true, "packageCycles": true,
}

// The result and parameter types. A descriptor-taking function is
// declared over each descriptor proto type it accepts, so a wrong
// kind is a type error at compile time and a result is typed, and
// over null, so a null argument — an absent side, a lookup that
// found nothing — yields null at evaluation. The checker types the
// result by its declaration, so a rule tests the argument for null
// (`parent(x) == null`) or converts (`dyn(comments(x)) == null`)
// rather than comparing a typed result with null; a kind a function
// is not declared over reaches the null overload's declaration at
// check time where the argument is dynamic, and is refused at
// evaluation.
var (
	dyn      = cel.DynType
	strList  = cel.ListType(cel.StringType)
	dynList  = cel.ListType(dyn)
	strDyn   = cel.MapType(cel.StringType, dyn)
	featType = cel.ObjectType("google.protobuf.FeatureSet")
	msgType  = cel.ObjectType("google.protobuf.DescriptorProto")
	// Every descriptor proto type an entity binds.
	descTypes = []*cel.Type{
		fileType, msgType,
		cel.ObjectType("google.protobuf.FieldDescriptorProto"),
		cel.ObjectType("google.protobuf.OneofDescriptorProto"),
		cel.ObjectType("google.protobuf.EnumDescriptorProto"),
		cel.ObjectType("google.protobuf.EnumValueDescriptorProto"),
		cel.ObjectType("google.protobuf.ServiceDescriptorProto"),
		cel.ObjectType("google.protobuf.MethodDescriptorProto"),
	}
	fileOnly = []*cel.Type{fileType}
	walkable = []*cel.Type{fileType, filesType, msgType}
)

// unary declares one unary function over each parameter type and
// over null.
func unary(name string, params []*cel.Type, ret *cel.Type, f func(ref.Val) ref.Val) cel.EnvOption {
	opts := make([]cel.FunctionOpt, 0, len(params)+1)
	opts = append(opts, cel.Overload(name+"_null", []*cel.Type{cel.NullType}, cel.NullType, cel.UnaryBinding(f)))
	for _, t := range params {
		id := name + "_" + strings.NewReplacer(".", "_", "(", "_", ")", "", ",", "_", " ", "").Replace(t.String())
		opts = append(opts, cel.Overload(id, []*cel.Type{t}, ret, cel.UnaryBinding(f)))
	}
	return cel.Function(name, opts...)
}

// library declares pb's functions over the environment's sets.
func (e *Env) library() []cel.EnvOption {
	// entity is a function over one declaration of the schema.
	entity := func(name string, params []*cel.Type, ret *cel.Type, f func(*entry) ref.Val) cel.EnvOption {
		return unary(name, params, ret, func(v ref.Val) ref.Val {
			en, out := e.entityArg(v)
			if en == nil {
				return out
			}
			return f(en)
		})
	}
	// walk is a function over a file, a list of files, or a message,
	// listing the declarations of one kind under it.
	walk := func(name string, kind func(*entry) []proto.Message) cel.EnvOption {
		return unary(name, walkable, dynList, func(v ref.Val) ref.Val { return e.declarations(v, kind) })
	}
	str := func(name string, f func(string) ref.Val) cel.EnvOption {
		return cel.Function(name, cel.Overload(name+"_string", []*cel.Type{cel.StringType}, dyn, cel.UnaryBinding(func(v ref.Val) ref.Val {
			return f(string(v.(types.String)))
		})))
	}
	return []cel.EnvOption{
		entity("comments", descTypes, strDyn, e.comments),
		entity("parent", descTypes, dyn, func(en *entry) ref.Val {
			if en.isFile() {
				return types.NullValue
			}
			return e.value(en.set.entryOf(en.desc.Parent()).msg)
		}),
		entity("file", descTypes, fileType, func(en *entry) ref.Val { return e.value(en.file.proto) }),
		entity("fullName", descTypes, cel.StringType, func(en *entry) ref.Val {
			if en.isFile() {
				return types.String(en.file.fd.Path())
			}
			return types.String(en.desc.FullName())
		}),
		walk("messages", func(en *entry) []proto.Message { return en.under(kindMessage) }),
		walk("enums", func(en *entry) []proto.Message { return en.under(kindEnum) }),
		walk("extensions", func(en *entry) []proto.Message { return en.under(kindExtension) }),
		walk("services", func(en *entry) []proto.Message { return en.under(kindService) }),
		str("resolve", func(name string) ref.Val {
			// A map-entry message is the compiler's: no rule resolves it.
			if en := e.new.byName[protoreflect.FullName(strings.TrimPrefix(name, "."))]; en != nil && !isMapEntry(en.desc) {
				return e.value(en.msg)
			}
			return types.NullValue
		}),
		str("fileByName", func(path string) ref.Val {
			if f := e.new.byPath[path]; f != nil {
				return e.value(f.proto)
			}
			return types.NullValue
		}),
		entity("imports", fileOnly, filesType, func(en *entry) ref.Val { return e.files(en.file.imports()) }),
		entity("visible", fileOnly, filesType, func(en *entry) ref.Val { return e.files(en.file.visible()) }),
		entity("references", fileOnly, strList, func(en *entry) ref.Val { return e.adapter.NativeToValue(en.file.references()) }),
		entity("features", descTypes, featType, e.features),
		entity("options", descTypes, strDyn, e.options),
		cel.Function("words", cel.Overload("words_string", []*cel.Type{cel.StringType}, strList, cel.UnaryBinding(func(v ref.Val) ref.Val {
			return e.adapter.NativeToValue(Words(string(v.(types.String))))
		}))),
		cel.Function("case", cel.Overload("case_string_string", []*cel.Type{cel.StringType, cel.StringType}, cel.StringType, cel.BinaryBinding(func(name, style ref.Val) ref.Val {
			out, ok := Case(string(name.(types.String)), string(style.(types.String)))
			if !ok {
				return types.NewErr("case: %q is no style (pascal, camel, snake, upper-snake)", style.(types.String))
			}
			return types.String(out)
		}))),
		cel.Function("packageCycles", cel.Overload("packageCycles_list", []*cel.Type{filesType}, cel.ListType(strList), cel.UnaryBinding(func(v ref.Val) ref.Val {
			files, err := e.fileArgs(v)
			if err != nil {
				return err
			}
			cycles := packageCycles(files)
			out := make([]any, len(cycles))
			for i, c := range cycles {
				out[i] = c
			}
			return e.adapter.NativeToValue(out)
		}))),
	}
}

func (e *Env) files(fs []*fileEntry) ref.Val {
	out := make([]any, len(fs))
	for i, f := range fs {
		out[i] = f.proto
	}
	return e.adapter.NativeToValue(out)
}

// fileArgs reads a list of file descriptor protos the sets built.
func (e *Env) fileArgs(v ref.Val) ([]*fileEntry, ref.Val) {
	lister, ok := v.(traits.Lister)
	if !ok {
		return nil, types.NewErr("%s is not a list of files", v.Type().TypeName())
	}
	out := []*fileEntry{}
	for it := lister.Iterator(); it.HasNext() == types.True; {
		en, errVal := e.entityArg(it.Next())
		if en == nil {
			return nil, types.NewErr("a file list holds %s", errVal.Type().TypeName())
		}
		if !en.isFile() {
			return nil, types.NewErr("%s is not a file", en.desc.FullName())
		}
		out = append(out, en.file)
	}
	return out, nil
}

// declarations answers messages/enums/extensions/services over a
// file, a list of files, or a message.
func (e *Env) declarations(v ref.Val, kind func(*entry) []proto.Message) ref.Val {
	if v == types.NullValue {
		return types.NullValue
	}
	out := []any{}
	if _, isList := v.(traits.Lister); isList {
		files, err := e.fileArgs(v)
		if err != nil {
			return err
		}
		for _, f := range files {
			for _, m := range kind(&f.entry) {
				out = append(out, m)
			}
		}
	} else {
		en, errVal := e.entityArg(v)
		if en == nil {
			return errVal
		}
		for _, m := range kind(en) {
			out = append(out, m)
		}
	}
	return e.adapter.NativeToValue(out)
}

func isMapEntry(d protoreflect.Descriptor) bool {
	md, ok := d.(protoreflect.MessageDescriptor)
	return ok && md.IsMapEntry()
}

// A declaration kind the walk lists.
type declKind int

const (
	kindMessage declKind = iota
	kindEnum
	kindExtension
	kindService
)

// under lists the declarations of one kind under a file or a message
// — the receiver excluded, nested ones included, map-entry messages
// excluded (they are the compiler's, never the author's) and not
// descended into, in declaration order.
func (en *entry) under(kind declKind) []proto.Message {
	out := []proto.Message{}
	add := func(d protoreflect.Descriptor) { out = append(out, en.set.entryOf(d).msg) }
	var messages func(ms protoreflect.MessageDescriptors)
	messages = func(ms protoreflect.MessageDescriptors) {
		for i := 0; i < ms.Len(); i++ {
			m := ms.Get(i)
			if m.IsMapEntry() {
				continue
			}
			if kind == kindMessage {
				add(m)
			}
			en.children(m, kind, add)
			messages(m.Messages())
		}
	}
	switch d := en.desc.(type) {
	case protoreflect.FileDescriptor:
		en.children(d, kind, add)
		messages(d.Messages())
	case protoreflect.MessageDescriptor:
		en.children(d, kind, add)
		messages(d.Messages())
	}
	return out
}

// children adds a scope's own declarations of a kind other than
// message: its enums, its extensions, or (a file's) its services.
func (en *entry) children(scope protoreflect.Descriptor, kind declKind, add func(protoreflect.Descriptor)) {
	type enumsAndExtensions interface {
		Enums() protoreflect.EnumDescriptors
		Extensions() protoreflect.ExtensionDescriptors
	}
	s := scope.(enumsAndExtensions)
	switch kind {
	case kindEnum:
		for i, es := 0, s.Enums(); i < es.Len(); i++ {
			add(es.Get(i))
		}
	case kindExtension:
		for i, xs := 0, s.Extensions(); i < xs.Len(); i++ {
			add(xs.Get(i))
		}
	case kindService:
		if fd, ok := scope.(protoreflect.FileDescriptor); ok {
			for i, ss := 0, fd.Services(); i < ss.Len(); i++ {
				add(ss.Get(i))
			}
		}
	}
}

func (e *Env) comments(en *entry) ref.Val {
	loc := en.file.fd.SourceLocations().ByDescriptor(en.desc)
	detached := loc.LeadingDetachedComments
	if detached == nil {
		detached = []string{}
	}
	return e.adapter.NativeToValue(map[string]any{
		"leading":  loc.LeadingComments,
		"trailing": loc.TrailingComments,
		"detached": detached,
	})
}

func (f *fileEntry) imports() []*fileEntry {
	out := []*fileEntry{}
	imps := f.fd.Imports()
	for i := 0; i < imps.Len(); i++ {
		out = append(out, f.set.byPath[imps.Get(i).Path()])
	}
	return out
}

// visible is the file, the files it imports, and then the files those
// make visible through public imports, transitively, in that order,
// each once.
func (f *fileEntry) visible() []*fileEntry {
	seen := map[*fileEntry]bool{f: true}
	out := []*fileEntry{f}
	direct := f.imports()
	for _, d := range direct {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	var public func(g *fileEntry)
	public = func(g *fileEntry) {
		imps := g.fd.Imports()
		for i := 0; i < imps.Len(); i++ {
			imp := imps.Get(i)
			dep := g.set.byPath[imp.Path()]
			if !imp.IsPublic || seen[dep] {
				continue
			}
			seen[dep] = true
			out = append(out, dep)
			public(dep)
		}
	}
	for _, d := range direct {
		public(d)
	}
	return out
}

// references walks the file — its options, then each message (its
// options, fields, oneofs, enums, extensions, nested messages), the
// enums, the extensions, the services with their methods, each list
// in declaration order — collecting the fully qualified names it
// references: custom option extensions on every options message,
// extendees, field and extension types (a map field's its value's),
// method request and response types; each once in first-use order,
// a declaration's type before its options.
func (f *fileEntry) references() []string {
	fd := f.fd
	seen := map[protoreflect.FullName]bool{}
	out := []string{}
	add := func(n protoreflect.FullName) {
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, string(n))
	}
	opts := func(d protoreflect.Descriptor) {
		if o := d.Options(); o != nil {
			proto.RangeExtensions(o, func(xt protoreflect.ExtensionType, _ any) bool {
				add(xt.TypeDescriptor().FullName())
				return true
			})
		}
	}
	field := func(fld protoreflect.FieldDescriptor) {
		if fld.IsExtension() {
			add(fld.ContainingMessage().FullName())
		}
		typed := fld
		if fld.IsMap() {
			typed = fld.MapValue()
		}
		switch typed.Kind() {
		case protoreflect.MessageKind, protoreflect.GroupKind:
			add(typed.Message().FullName())
		case protoreflect.EnumKind:
			add(typed.Enum().FullName())
		}
		opts(fld)
	}
	enums := func(es protoreflect.EnumDescriptors) {
		for i := 0; i < es.Len(); i++ {
			opts(es.Get(i))
			for j, vs := 0, es.Get(i).Values(); j < vs.Len(); j++ {
				opts(vs.Get(j))
			}
		}
	}
	var messages func(protoreflect.MessageDescriptors)
	messages = func(ms protoreflect.MessageDescriptors) {
		for i := 0; i < ms.Len(); i++ {
			m := ms.Get(i)
			opts(m)
			for j, fs := 0, m.Fields(); j < fs.Len(); j++ {
				field(fs.Get(j))
			}
			for j, os := 0, m.Oneofs(); j < os.Len(); j++ {
				opts(os.Get(j))
			}
			enums(m.Enums())
			for j, xs := 0, m.Extensions(); j < xs.Len(); j++ {
				field(xs.Get(j))
			}
			messages(m.Messages())
		}
	}
	opts(fd)
	messages(fd.Messages())
	enums(fd.Enums())
	for i, xs := 0, fd.Extensions(); i < xs.Len(); i++ {
		field(xs.Get(i))
	}
	for i, ss := 0, fd.Services(); i < ss.Len(); i++ {
		s := ss.Get(i)
		opts(s)
		for j, ms := 0, s.Methods(); j < ms.Len(); j++ {
			m := ms.Get(j)
			add(m.Input().FullName())
			add(m.Output().FullName())
			opts(m)
		}
	}
	return out
}

// features is the entity's resolved FeatureSet: each of the message's
// own fields resolved through the editions inheritance chain, and a
// field's modifiers — `optional` and oneof membership in proto3,
// `required`, a group, `packed` — as the features they imply, which
// the resolver, reading a proto2 or proto3 file's syntax alone, does
// not see; in an editions file the modifiers are the resolved
// features already, so the two agree.
func (e *Env) features(en *entry) ref.Val {
	fs := &descriptorpb.FeatureSet{}
	m := fs.ProtoReflect()
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		v, err := protoutil.ResolveFeature(en.desc, f)
		if err != nil {
			return types.NewErr("features: %s: %v", f.Name(), err)
		}
		if v.IsValid() {
			m.Set(f, v)
		}
	}
	if fd, ok := en.desc.(protoreflect.FieldDescriptor); ok {
		modifierFeatures(fs, fd)
	}
	return e.adapter.NativeToValue(fs)
}

// modifierFeatures applies a field's modifiers.
func modifierFeatures(fs *descriptorpb.FeatureSet, fd protoreflect.FieldDescriptor) {
	switch {
	case fd.Cardinality() == protoreflect.Required:
		fs.FieldPresence = descriptorpb.FeatureSet_LEGACY_REQUIRED.Enum()
	case fd.Syntax() == protoreflect.Proto3 && (fd.HasOptionalKeyword() || fd.ContainingOneof() != nil && !fd.ContainingOneof().IsSynthetic()):
		// The optional keyword and a oneof both give a proto3 field
		// explicit presence.
		fs.FieldPresence = descriptorpb.FeatureSet_EXPLICIT.Enum()
	}
	if fd.Kind() == protoreflect.GroupKind {
		fs.MessageEncoding = descriptorpb.FeatureSet_DELIMITED.Enum()
	}
	if fd.IsList() && !fd.IsMap() {
		switch fd.Kind() {
		case protoreflect.MessageKind, protoreflect.GroupKind, protoreflect.StringKind, protoreflect.BytesKind:
			// Never packable: the inherited feature stands, as the
			// descriptor would report it.
		default:
			if fd.IsPacked() {
				fs.RepeatedFieldEncoding = descriptorpb.FeatureSet_PACKED.Enum()
			} else {
				fs.RepeatedFieldEncoding = descriptorpb.FeatureSet_EXPANDED.Enum()
			}
		}
	}
}

// options is the entity's options as a map: a built-in option under
// its field name, a custom option under its extension's fully
// qualified name in parentheses, as the language writes it; exactly
// the options set.
func (e *Env) options(en *entry) ref.Val {
	return e.adapter.NativeToValue(optionsOf(en.desc.Options()))
}

func optionsOf(o proto.Message) map[string]any {
	out := map[string]any{}
	if o == nil {
		return out
	}
	o.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		out[optionKey(fd)] = optionValue(fd, v)
		return true
	})
	return out
}

func optionKey(fd protoreflect.FieldDescriptor) string {
	if fd.IsExtension() {
		return "(" + string(fd.FullName()) + ")"
	}
	return string(fd.Name())
}

// optionValue converts an option value by its shape: a repeated a
// list, a map field a map by key, a message a map by field name (an
// extension's key parenthesized), an enum its name, a scalar its
// value.
func optionValue(fd protoreflect.FieldDescriptor, v protoreflect.Value) any {
	switch {
	case fd.IsList():
		l := v.List()
		out := make([]any, l.Len())
		for i := range out {
			out[i] = elementValue(fd, l.Get(i))
		}
		return out
	case fd.IsMap():
		out := map[string]any{}
		v.Map().Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
			out[fmt.Sprint(k.Interface())] = elementValue(fd.MapValue(), mv)
			return true
		})
		return out
	}
	return elementValue(fd, v)
}

// elementValue converts one element of a field's value.
func elementValue(fd protoreflect.FieldDescriptor, v protoreflect.Value) any {
	switch fd.Kind() {
	case protoreflect.EnumKind:
		if ev := fd.Enum().Values().ByNumber(v.Enum()); ev != nil {
			return string(ev.Name())
		}
		return int64(v.Enum())
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return optionsOf(v.Message().Interface())
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return int64(v.Int())
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return v.Uint()
	case protoreflect.FloatKind:
		return v.Float()
	}
	return v.Interface()
}

// packageCycles is the strongly connected components of two or more
// packages in the files' package import graph, each component its
// package names sorted, the components sorted by first name.
func packageCycles(files []*fileEntry) [][]string {
	edges := map[string]map[string]bool{}
	var order []string
	node := func(p string) {
		if _, ok := edges[p]; !ok {
			edges[p] = map[string]bool{}
			order = append(order, p)
		}
	}
	for _, f := range files {
		p := string(f.fd.Package())
		node(p)
		imps := f.fd.Imports()
		for i := 0; i < imps.Len(); i++ {
			q := string(imps.Get(i).Package())
			node(q)
			edges[p][q] = true
		}
	}
	sort.Strings(order)
	// Tarjan's algorithm over the packages in sorted order.
	index := map[string]int{}
	low := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	out := [][]string{}
	next := 0
	var visit func(v string)
	visit = func(v string) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		targets := make([]string, 0, len(edges[v]))
		for w := range edges[v] {
			targets = append(targets, w)
		}
		sort.Strings(targets)
		for _, w := range targets {
			if _, seen := index[w]; !seen {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		var comp []string
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			comp = append(comp, w)
			if w == v {
				break
			}
		}
		// A component of one package is no cycle: an import within a
		// package is a self-edge the definition never counts.
		if len(comp) >= 2 {
			sort.Strings(comp)
			out = append(out, comp)
		}
	}
	for _, v := range order {
		if _, seen := index[v]; !seen {
			visit(v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

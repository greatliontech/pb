package env1

import (
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"github.com/bufbuild/protocompile/protoutil"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// A function of pb's library (REQ-env1-library): its name, which the
// cost tracker charges by size, and its declaration.
type function struct {
	name string
	opt  cel.EnvOption
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
func (e *Env) library() []function {
	// entity is a function over one declaration of the schema.
	entity := func(name string, params []*cel.Type, ret *cel.Type, f func(*entry) ref.Val) function {
		return function{name, unary(name, params, ret, func(v ref.Val) ref.Val {
			en, out := e.entityArg(v)
			if en == nil {
				return out
			}
			return f(en)
		})}
	}
	// walk is a function over a file, a list of files, or a message,
	// listing the declarations of one kind under it.
	walk := func(name string, kind func(*entry) []proto.Message) function {
		return function{name, unary(name, walkable, dynList, func(v ref.Val) ref.Val { return e.declarations(v, kind) })}
	}
	str := func(name string, f func(string) ref.Val) function {
		return function{name, cel.Function(name, cel.Overload(name+"_string", []*cel.Type{cel.StringType}, dyn, cel.UnaryBinding(func(v ref.Val) ref.Val {
			return f(string(v.(types.String)))
		})))}
	}
	return []function{
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
		entity("syntax", fileOnly, cel.StringType, func(en *entry) ref.Val { return types.String(declaredSyntax(en.file.fd)) }),
		entity("dir", fileOnly, cel.StringType, func(en *entry) ref.Val {
			d := path.Dir(en.file.fd.Path())
			if d == "." {
				d = ""
			}
			return types.String(d)
		}),
		function{"unique", cel.Function("unique", cel.Overload("unique_list", []*cel.Type{dynList}, dynList, cel.UnaryBinding(func(v ref.Val) ref.Val {
			return e.unique(v)
		})))},
		entity("options", descTypes, strDyn, e.options),
		function{"words", cel.Function("words", cel.Overload("words_string", []*cel.Type{cel.StringType}, strList, cel.UnaryBinding(func(v ref.Val) ref.Val {
			return e.adapter.NativeToValue(Words(string(v.(types.String))))
		})))},
		function{"case", cel.Function("case", cel.Overload("case_string_string", []*cel.Type{cel.StringType, cel.StringType}, cel.StringType, cel.BinaryBinding(func(name, style ref.Val) ref.Val {
			out, ok := Case(string(name.(types.String)), string(style.(types.String)))
			if !ok {
				return types.NewErr("case: %q is no style (pascal, camel, snake, upper-snake)", style.(types.String))
			}
			return types.String(out)
		})))},
		function{"packageCycles", cel.Function("packageCycles", cel.Overload("packageCycles_list", []*cel.Type{filesType}, cel.ListType(strList), cel.UnaryBinding(func(v ref.Val) ref.Val {
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
		})))},
	}
}

// unique is the list's distinct members in first-seen order, two
// members one under CEL's equality: each keyed by a canonical
// spelling — a number by its value whatever its type, so 1, 1u and
// 1.0 are one member, an integer beyond int64 spelled as the exact
// integer it is; a string, bytes, bool, null, type, duration or
// timestamp by kind and value; a message by its type and
// deterministic encoding; a list or map by its members' keys, each
// length-prefixed, a map's in key order — so the pass is one over
// the list and its members' extent. A NaN equals nothing, itself
// included, so every NaN member stays. A member of no such kind is
// refused.
func (e *Env) unique(v ref.Val) ref.Val {
	l, ok := v.(traits.Lister)
	if !ok {
		return types.NewErr("unique: %s is not a list", v.Type().TypeName())
	}
	seen := map[string]bool{}
	var out []ref.Val
	k := keyer{}
	for it := l.Iterator(); it.HasNext() == types.True; {
		m := it.Next()
		key, err := k.key(m)
		if err != nil {
			return types.NewErr("unique: %v", err)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, m)
	}
	return e.adapter.NativeToValue(out)
}

// keyer spells members canonically, each NaN its own.
type keyer struct{ nans int }

// key is a member's canonical spelling, equal for two members CEL
// holds equal, distinct otherwise.
func (k *keyer) key(m ref.Val) (string, error) {
	if m.Type() == types.NullType {
		return "null", nil
	}
	if m.Type() == types.TypeType {
		return "t" + m.(ref.Type).TypeName(), nil
	}
	switch x := m.Value().(type) {
	case bool:
		return fmt.Sprintf("b%t", x), nil
	case int64:
		return "n" + strconv.FormatInt(x, 10), nil
	case uint64:
		return "n" + strconv.FormatUint(x, 10), nil
	case float64:
		switch {
		case math.IsNaN(x):
			k.nans++
			return "nan" + strconv.Itoa(k.nans), nil
		case x == math.Trunc(x) && x >= math.MinInt64 && x < math.MaxInt64:
			return "n" + strconv.FormatInt(int64(x), 10), nil
		case x == math.Trunc(x) && x >= 0 && x < math.MaxUint64:
			return "n" + strconv.FormatUint(uint64(x), 10), nil
		}
		return "f" + strconv.FormatFloat(x, 'g', -1, 64), nil
	case string:
		return "s" + x, nil
	case []byte:
		return "y" + string(x), nil
	case time.Duration:
		return "d" + strconv.FormatInt(int64(x), 10), nil
	case time.Time:
		return "T" + strconv.FormatInt(x.Unix(), 10) + "." + strconv.Itoa(x.Nanosecond()), nil
	case proto.Message:
		b, err := proto.MarshalOptions{Deterministic: true}.Marshal(x)
		if err != nil {
			return "", err
		}
		return "m" + string(m.Type().TypeName()) + "\x00" + string(b), nil
	}
	sized := func(s string) string { return strconv.Itoa(len(s)) + ":" + s }
	switch l := m.(type) {
	case traits.Mapper:
		var entries []string
		for it := l.Iterator(); it.HasNext() == types.True; {
			mk := it.Next()
			kk, err := k.key(mk)
			if err != nil {
				return "", err
			}
			vk, err := k.key(l.Get(mk))
			if err != nil {
				return "", err
			}
			entries = append(entries, sized(kk)+sized(vk))
		}
		sort.Strings(entries)
		return "{" + strings.Join(entries, "") + "}", nil
	case traits.Lister:
		var members []string
		for it := l.Iterator(); it.HasNext() == types.True; {
			mk, err := k.key(it.Next())
			if err != nil {
				return "", err
			}
			members = append(members, sized(mk))
		}
		return "[" + strings.Join(members, "") + "]", nil
	}
	return "", fmt.Errorf("a %s member has no equality unique can key", m.Type().TypeName())
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

// declaredSyntax is the syntax a file declares — proto2, proto3 or
// editions — or empty where it declares none: a descriptor spells no
// proto2, so a proto2 declaration is read from the source
// information's location of the syntax statement.
func declaredSyntax(fd protoreflect.FileDescriptor) string {
	switch fd.Syntax() {
	case protoreflect.Proto3:
		return "proto3"
	case protoreflect.Editions:
		return "editions"
	}
	// The syntax statement is FileDescriptorProto field 12; a location
	// the file's source information lacks comes back with no path.
	if len(fd.SourceLocations().ByPath(protoreflect.SourcePath{12}).Path) == 0 {
		return ""
	}
	return "proto2"
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
	return e.adapter.NativeToValue(e.withLanguageFeatures(en, fs))
}

// withLanguageFeatures is the resolved FeatureSet carrying every
// language feature the environment knows: each an extension of
// FeatureSet in the runtime's family, the registry's, resolved
// through the entity's own side's declaration of it — the standard
// declaration where the side has none, whose value is then the
// edition's default — and set on the feature set field by field.
func (e *Env) withLanguageFeatures(en *entry, fs *descriptorpb.FeatureSet) proto.Message {
	for _, xt := range e.featureExts {
		own, declared := en.set.featureExts[xt.TypeDescriptor().FullName()]
		if !declared {
			own = xt
		}
		msg := dynamicpb.NewMessage(xt.TypeDescriptor().Message())
		fields := own.TypeDescriptor().Message().Fields()
		for i := 0; i < fields.Len(); i++ {
			f := fields.Get(i)
			v, err := protoutil.ResolveCustomFeature(en.desc, own, f)
			if err != nil || !v.IsValid() {
				continue
			}
			if g := msg.Descriptor().Fields().ByName(f.Name()); g != nil {
				msg.Set(g, v)
			}
		}
		fs.ProtoReflect().Set(xt.TypeDescriptor(), protoreflect.ValueOfMessage(msg))
	}
	return fs
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

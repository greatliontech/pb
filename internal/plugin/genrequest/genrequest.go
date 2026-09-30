// Package genrequest builds a plugin's CodeGeneratorRequest from a
// compiled build (generation.md REQ-gen-request) with declared option
// overrides applied first (REQ-gen-overrides-declarative). The request
// is a pure function of the compiled descriptor set — its topological
// order the compile's, handed in — the applied overrides, and the
// entry's parameter string (REQ-gen-request-determinism): no compiler
// version, no timestamps, no environment — an input the spec does not
// name never enters. Each call converts descriptors fresh from the
// compiled set, so one entry's overrides never leak into another's
// request.
package genrequest

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/bufbuild/protocompile/linker"
	"github.com/greatliontech/glob"
	"github.com/greatliontech/pb/internal/plugin/genfile"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// Build returns the request for one generation entry over the compiled
// workspace files (REQ-gen-request): file_to_generate the entry's
// targets — the workspace files its patterns select in compile order,
// then, where it includes imports, what those reach that is no
// well-known import (wellKnown judges) in the order given — proto_file
// in the order given — the compile's topological order over files,
// dependencies before importers, imports visited in declaration order
// — source_file_descriptors the targets' descriptors, and the entry's
// opt as the parameter verbatim. Each descriptor is converted fresh
// from the linked file, so this entry's overrides touch this request
// alone.
func Build(order []protoreflect.FileDescriptor, files linker.Files, overrides []genfile.Override, entry genfile.Plugin, wellKnown func(path string) bool) (*pluginpb.CodeGeneratorRequest, error) {
	protos := make(map[string]*descriptorpb.FileDescriptorProto, len(order))
	for _, fd := range order {
		protos[fd.Path()] = protodesc.ToFileDescriptorProto(fd)
	}
	// The order is the compile's over these files, one fact handed in
	// twice across the domain boundary: a file the order lacks would
	// leave a nil descriptor in the request.
	for _, f := range files {
		if protos[f.Path()] == nil {
			return nil, fmt.Errorf("genrequest: %s is a workspace file but not in the compiled order", f.Path())
		}
	}
	targets, err := Targets(order, files, entry, wellKnown)
	if err != nil {
		return nil, err
	}
	if err := applyOverrides(order, protos, overrides, extensionResolver(order), wellKnown); err != nil {
		return nil, err
	}
	req := &pluginpb.CodeGeneratorRequest{FileToGenerate: targets}
	if entry.Opt != "" {
		req.Parameter = &entry.Opt
	}
	for _, fd := range order {
		req.ProtoFile = append(req.ProtoFile, protos[fd.Path()])
	}
	for _, t := range targets {
		req.SourceFileDescriptors = append(req.SourceFileDescriptors, protos[t])
	}
	return req, nil
}

// Targets names the entry's generation targets (REQ-gen-request, the
// generation target term): the workspace files whose module-relative
// path any of the entry's patterns matches, every workspace file with
// no pattern, in files' order — a pattern matching nothing fails
// naming it, whatever the others match — then, where
// the entry includes imports, every file the selected reach through
// imports that is no well-known import and not selected, in order's
// order.
func Targets(order []protoreflect.FileDescriptor, files linker.Files, entry genfile.Plugin, wellKnown func(path string) bool) ([]string, error) {
	patterns := make([]*glob.Pattern, 0, len(entry.Files))
	for _, f := range entry.Files {
		p, err := glob.Compile(f)
		if err != nil {
			// Validated at parse time; a compile failure here is a
			// genfile invariant violation, not user input.
			return nil, err
		}
		patterns = append(patterns, p)
	}
	selected := map[string]bool{}
	var targets []string
	live := make([]bool, len(patterns))
	for _, f := range files {
		matched := len(patterns) == 0
		for i, p := range patterns {
			if p.Match(f.Path()) {
				matched, live[i] = true, true
			}
		}
		if matched {
			selected[f.Path()] = true
			targets = append(targets, f.Path())
		}
	}
	var dead []string
	for i, ok := range live {
		if !ok {
			dead = append(dead, entry.Files[i])
		}
	}
	switch len(dead) {
	case 0:
	case 1:
		return nil, fmt.Errorf("genrequest: the pattern %q selects no workspace file", dead[0])
	default:
		return nil, fmt.Errorf("genrequest: the patterns %q select no workspace file", dead)
	}
	if !entry.IncludeImports {
		return targets, nil
	}
	reached := map[string]bool{}
	var walk func(fd protoreflect.FileDescriptor)
	walk = func(fd protoreflect.FileDescriptor) {
		imports := fd.Imports()
		for i := 0; i < imports.Len(); i++ {
			dep := imports.Get(i).FileDescriptor
			if reached[dep.Path()] {
				continue
			}
			reached[dep.Path()] = true
			walk(dep)
		}
	}
	for _, f := range files {
		if selected[f.Path()] {
			walk(f)
		}
	}
	for _, fd := range order {
		if reached[fd.Path()] && !selected[fd.Path()] && !wellKnown(fd.Path()) {
			targets = append(targets, fd.Path())
		}
	}
	return targets, nil
}

// extensionResolver indexes every extension declaration reachable in
// the compiled set — top-level and message-nested — by full name.
func extensionResolver(order []protoreflect.FileDescriptor) func(protoreflect.FullName) (protoreflect.ExtensionDescriptor, bool) {
	index := map[protoreflect.FullName]protoreflect.ExtensionDescriptor{}
	var addMsg func(md protoreflect.MessageDescriptor)
	add := func(xds protoreflect.ExtensionDescriptors) {
		for i := 0; i < xds.Len(); i++ {
			xd := xds.Get(i)
			index[xd.FullName()] = xd
		}
	}
	addMsg = func(md protoreflect.MessageDescriptor) {
		add(md.Extensions())
		for i := 0; i < md.Messages().Len(); i++ {
			addMsg(md.Messages().Get(i))
		}
	}
	for _, fd := range order {
		add(fd.Extensions())
		for i := 0; i < fd.Messages().Len(); i++ {
			addMsg(fd.Messages().Get(i))
		}
	}
	return func(name protoreflect.FullName) (protoreflect.ExtensionDescriptor, bool) {
		xd, ok := index[name]
		return xd, ok
	}
}

// applyOverrides mutates the matching files' options in the converted
// protos (REQ-gen-overrides-declarative): entries in declaration
// order, later entries winning; matching is over include-root-relative
// paths — the names proto_file carries — workspace and dependency
// files alike.
func applyOverrides(order []protoreflect.FileDescriptor, protos map[string]*descriptorpb.FileDescriptorProto, overrides []genfile.Override, ext func(protoreflect.FullName) (protoreflect.ExtensionDescriptor, bool), wellKnown func(string) bool) error {
	for _, o := range overrides {
		p, err := glob.Compile(o.Files)
		if err != nil {
			// Validated at parse time; a compile failure here is a
			// genfile invariant violation, not user input.
			return err
		}
		for _, fd := range order {
			// A well-known import is the toolchain's, its options its
			// own: never overridden (REQ-gen-overrides-declarative).
			if wellKnown(fd.Path()) || !p.Match(fd.Path()) {
				continue
			}
			value := o.Value
			if o.Derived() {
				v, ok := o.Derive(fd.Path(), string(fd.Package()))
				if !ok {
					continue
				}
				value = v
			}
			fdp := protos[fd.Path()]
			if fdp.Options == nil {
				fdp.Options = &descriptorpb.FileOptions{}
			}
			if err := setOption(fdp.Options, o.Option, value, ext); err != nil {
				return fmt.Errorf("override %q on %s: %w", o.Option, fd.Path(), err)
			}
		}
	}
	return nil
}

// setOption assigns one file option by name: built-in fields by field
// name, custom options through the compiled set's extension
// declarations — "(pkg.ext)" for a scalar extension, "(pkg.ext).field"
// for one field of a message-typed extension. A name resolving to
// nothing, a non-scalar target, or a value outside the field's kind
// fails; nothing is guessed.
func setOption(opts *descriptorpb.FileOptions, name, value string, ext func(protoreflect.FullName) (protoreflect.ExtensionDescriptor, bool)) error {
	m := opts.ProtoReflect()
	extension, field, builtin, err := genfile.SplitOption(name)
	if err != nil {
		// The name was validated at parse time; the shared grammar makes
		// this arm a genfile invariant violation, reported, not guessed.
		return err
	}
	if builtin != "" {
		fd := m.Descriptor().Fields().ByName(protoreflect.Name(builtin))
		if fd == nil {
			return errors.New("no such file option")
		}
		v, err := scalarValue(fd, value)
		if err != nil {
			return err
		}
		m.Set(fd, v)
		return nil
	}
	xd, ok := ext(protoreflect.FullName(extension))
	if !ok {
		return fmt.Errorf("no extension %s in the compiled set", extension)
	}
	if xd.ContainingMessage().FullName() != "google.protobuf.FileOptions" {
		return fmt.Errorf("%s extends %s, not FileOptions", extension, xd.ContainingMessage().FullName())
	}
	xt := dynamicpb.NewExtensionType(xd)
	if field == "" {
		v, err := scalarValue(xd, value)
		if err != nil {
			return err
		}
		m.Set(xt.TypeDescriptor(), v)
		return nil
	}
	// One level of field selection inside a message-typed extension;
	// deeper nesting waits for a demonstrated need.
	if xd.Kind() != protoreflect.MessageKind {
		return fmt.Errorf("%s is not message-typed; %q names no field", extension, field)
	}
	fd := xd.Message().Fields().ByName(protoreflect.Name(field))
	if fd == nil {
		return fmt.Errorf("%s has no field %q", extension, field)
	}
	v, err := scalarValue(fd, value)
	if err != nil {
		return err
	}
	m.Mutable(xt.TypeDescriptor()).Message().Set(fd, v)
	return nil
}

// scalarValue parses a value's spelling by the field's kind: strings
// verbatim, booleans as true/false, enums by value name, integers and
// floats by Go syntax. Repeated, map, and message-valued options are
// not overridable.
func scalarValue(fd protoreflect.FieldDescriptor, s string) (protoreflect.Value, error) {
	if fd.IsList() || fd.IsMap() {
		return protoreflect.Value{}, errors.New("repeated and map options are not overridable")
	}
	switch fd.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(s), nil
	case protoreflect.BytesKind:
		return protoreflect.ValueOfBytes([]byte(s)), nil
	case protoreflect.BoolKind:
		switch s {
		case "true":
			return protoreflect.ValueOfBool(true), nil
		case "false":
			return protoreflect.ValueOfBool(false), nil
		}
		return protoreflect.Value{}, fmt.Errorf("%q is not true or false", s)
	case protoreflect.EnumKind:
		ev := fd.Enum().Values().ByName(protoreflect.Name(s))
		if ev == nil {
			return protoreflect.Value{}, fmt.Errorf("%q is not a value of %s", s, fd.Enum().FullName())
		}
		return protoreflect.ValueOfEnum(ev.Number()), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfInt32(int32(n)), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfInt64(n), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfUint32(uint32(n)), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfUint64(n), nil
	case protoreflect.FloatKind:
		f, err := strconv.ParseFloat(s, 32)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfFloat32(float32(f)), nil
	case protoreflect.DoubleKind:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfFloat64(f), nil
	}
	return protoreflect.Value{}, fmt.Errorf("%s options are not overridable", fd.Kind())
}

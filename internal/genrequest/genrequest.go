// Package genrequest builds a plugin's CodeGeneratorRequest from a
// compiled build (generation.md REQ-gen-request) with declared option
// overrides applied first (REQ-gen-overrides-declarative). The request
// is a pure function of the compiled descriptor set, the applied
// overrides, and the entry's parameter string
// (REQ-gen-request-determinism): no compiler version, no timestamps,
// no environment — an input the spec does not name never enters. Each
// call converts descriptors fresh from the compiled set, so one
// entry's overrides never leak into another's request.
package genrequest

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/bufbuild/protocompile/linker"
	"github.com/greatliontech/glob"
	"github.com/greatliontech/pb/internal/genfile"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// Build returns the request for one generation entry over the compiled
// workspace files (REQ-gen-request): file_to_generate in compile
// order, proto_file topological with imports visited in declaration
// order, source_file_descriptors the generated files' descriptors,
// and opt as the parameter verbatim.
func Build(files linker.Files, overrides []genfile.Override, opt string) (*pluginpb.CodeGeneratorRequest, error) {
	order, protos := topological(files)
	if err := applyOverrides(order, protos, overrides, extensionResolver(order)); err != nil {
		return nil, err
	}
	req := &pluginpb.CodeGeneratorRequest{}
	for _, f := range files {
		req.FileToGenerate = append(req.FileToGenerate, f.Path())
	}
	if opt != "" {
		req.Parameter = &opt
	}
	for _, fd := range order {
		req.ProtoFile = append(req.ProtoFile, protos[fd.Path()])
	}
	for _, f := range files {
		req.SourceFileDescriptors = append(req.SourceFileDescriptors, protos[f.Path()])
	}
	return req, nil
}

// topological returns every file reachable from files — dependencies
// before importers, each file's imports visited in declaration order —
// with a freshly converted FileDescriptorProto per file. The order is
// a pure function of the file set.
func topological(files linker.Files) ([]protoreflect.FileDescriptor, map[string]*descriptorpb.FileDescriptorProto) {
	var order []protoreflect.FileDescriptor
	protos := map[string]*descriptorpb.FileDescriptorProto{}
	seen := map[string]bool{}
	var visit func(fd protoreflect.FileDescriptor)
	visit = func(fd protoreflect.FileDescriptor) {
		if seen[fd.Path()] {
			return
		}
		seen[fd.Path()] = true
		imps := fd.Imports()
		for i := 0; i < imps.Len(); i++ {
			visit(imps.Get(i).FileDescriptor)
		}
		order = append(order, fd)
		protos[fd.Path()] = protodesc.ToFileDescriptorProto(fd)
	}
	for _, f := range files {
		visit(f)
	}
	return order, protos
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
func applyOverrides(order []protoreflect.FileDescriptor, protos map[string]*descriptorpb.FileDescriptorProto, overrides []genfile.Override, ext func(protoreflect.FullName) (protoreflect.ExtensionDescriptor, bool)) error {
	for _, o := range overrides {
		p, err := glob.Compile(o.Files)
		if err != nil {
			// Validated at parse time; a compile failure here is a
			// genfile invariant violation, not user input.
			return err
		}
		for _, fd := range order {
			if !p.Match(fd.Path()) {
				continue
			}
			fdp := protos[fd.Path()]
			if fdp.Options == nil {
				fdp.Options = &descriptorpb.FileOptions{}
			}
			if err := setOption(fdp.Options, o.Option, o.Value, ext); err != nil {
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

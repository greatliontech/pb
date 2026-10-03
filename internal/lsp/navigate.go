package lsp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bufbuild/protocompile/ast"
	"github.com/bufbuild/protocompile/linker"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/greatliontech/lsp/protocol"
	"github.com/greatliontech/lsp/uri"
)

// index is what navigation reads of the last judgement that compiled
// (REQ-lsp-definition), built on the judgement's goroutine from the
// compile's retained syntax trees: every declaration by full name
// with its name token's place, every file's spans — the tokens that
// name a declaration, an import's path — and the judgement's file
// table, the bytes each file held and its origin. It holds
// everything a request reads, so a judgement committed between two
// reads of it changes nothing it answers.
type index struct {
	decls map[protoreflect.FullName]*decl
	spans map[string]*fileIndex // by include-root-relative path
	build *buildFiles
}

// decl is a declaration's name token in its file, with what hover
// says of it.
type decl struct {
	path       string
	start, end int
	kind       string
	comments   string
}

// fileIndex is one file's spans, sorted by start.
type fileIndex struct {
	path  string
	spans []span
}

// span is a token of a file that binds a name: a declaration's own
// name (decl), a reference to one (target), or an import's path
// (importPath).
type span struct {
	start, end int
	target     protoreflect.FullName
	importPath string
	decl       bool
}

// at is the span holding the byte offset — a cursor just past a
// name's last character is over the name — or nil; of spans sharing
// a token, the first recorded.
func (f *fileIndex) at(off int) *span {
	i := sort.Search(len(f.spans), func(i int) bool { return f.spans[i].end >= off })
	if i < len(f.spans) && f.spans[i].start <= off && off <= f.spans[i].end {
		return &f.spans[i]
	}
	return nil
}

// newIndex walks every linked file of the build — the targets and
// their imports, transitively — and records its declarations and
// the tokens that bind names, over the judgement's file table.
func newIndex(linked linker.Files, build *buildFiles) *index {
	idx := &index{decls: map[protoreflect.FullName]*decl{}, spans: map[string]*fileIndex{}, build: build}
	seen := map[string]bool{}
	var visit func(f linker.File)
	visit = func(f linker.File) {
		if f == nil || seen[f.Path()] {
			return
		}
		seen[f.Path()] = true
		imports := f.Imports()
		for i := 0; i < imports.Len(); i++ {
			visit(f.FindImportByPath(imports.Get(i).Path()))
		}
		if res, ok := f.(linker.Result); ok && res.AST() != nil {
			idx.file(res)
		}
	}
	for _, f := range linked {
		visit(f)
	}
	for _, fi := range idx.spans {
		// A token binding what no indexed file declares — a map entry's
		// field, a declaration with no source — leads nowhere and is
		// dropped; an import's path stays.
		kept := fi.spans[:0]
		for _, sp := range fi.spans {
			if sp.importPath != "" || idx.decls[sp.target] != nil {
				kept = append(kept, sp)
			}
		}
		fi.spans = kept
		sort.SliceStable(fi.spans, func(a, b int) bool { return fi.spans[a].start < fi.spans[b].start })
	}
	return idx
}

// walker indexes one linked file: the declarations through the
// descriptors and the parser's nodes for them, the references through
// the nodes' identifiers and what the linker bound them to.
type walker struct {
	idx      *index
	res      linker.Result
	tree     *ast.FileNode
	fi       *fileIndex
	resolver linker.Resolver
	extended map[*ast.ExtendNode]bool
}

func (idx *index) file(res linker.Result) {
	if _, ok := idx.build.byPath[res.Path()]; !ok {
		// A file whose bytes nothing holds is not indexed: its offsets
		// would name nothing.
		return
	}
	w := &walker{idx: idx, res: res, tree: res.AST(), fi: &fileIndex{path: res.Path()}, resolver: linker.ResolverFromFile(res), extended: map[*ast.ExtendNode]bool{}}
	idx.spans[res.Path()] = w.fi
	fdp := res.FileDescriptorProto()
	for _, d := range w.tree.Decls {
		switch n := d.(type) {
		case *ast.ImportNode:
			start, end := w.offsets(n.Name)
			w.fi.spans = append(w.fi.spans, span{start: start, end: end, importPath: n.Name.AsString()})
		case *ast.OptionNode:
			w.option(n, w.options("FileOptions"), res.Package(), nil)
		}
	}
	msgs := res.Messages()
	for i := 0; i < msgs.Len(); i++ {
		w.message(msgs.Get(i), fdp.MessageType[i])
	}
	enums := res.Enums()
	for i := 0; i < enums.Len(); i++ {
		w.enum(enums.Get(i), fdp.EnumType[i])
	}
	exts := res.Extensions()
	for i := 0; i < exts.Len(); i++ {
		w.field(exts.Get(i), fdp.Extension[i])
	}
	svcs := res.Services()
	for i := 0; i < svcs.Len(); i++ {
		w.service(svcs.Get(i), fdp.Service[i])
	}
}

// offsets is a node's byte span in the file, the end one past the
// node's last byte (the parser's End names the last byte).
func (w *walker) offsets(n ast.Node) (start, end int) {
	info := w.tree.NodeInfo(n)
	return info.Start().Offset, info.End().Offset + 1
}

// declare records a declaration's name token; node is the
// declaration, whose leading comments the hover shows.
func (w *walker) declare(d protoreflect.Descriptor, node, name ast.Node, kind string) {
	if name == nil {
		return
	}
	start, end := w.offsets(name)
	w.fi.spans = append(w.fi.spans, spanOf(start, end, d.FullName()))
	w.idx.decls[d.FullName()] = &decl{path: w.fi.path, start: start, end: end, kind: kind, comments: w.comments(node, d)}
}

// spanOf is a declaration's own span.
func spanOf(start, end int, name protoreflect.FullName) span {
	return span{start: start, end: end, target: name, decl: true}
}

// refer records a token that names the declaration.
func (w *walker) refer(n ast.Node, d protoreflect.Descriptor) {
	if n == nil || d == nil {
		return
	}
	start, end := w.offsets(n)
	w.fi.spans = append(w.fi.spans, span{start: start, end: end, target: d.FullName()})
}

// comments is a declaration's leading comments as the compiler
// records them — the `//` and `/* */` markers and a block's inner
// `*` margin gone — with what the markers leave behind stripped: a
// `/**` opening's `*` on the first line, a `**/` closing's on the
// last, the one space after a marker on each line and the one
// before a closing marker; a line's
// further indentation and trailing space stay (an indented example,
// markdown's hard break), empty edge lines go. The comments' kind is
// the declaration's attached group in the syntax tree: one block
// comment, or a run of `//` lines, as the compiler groups them.
func (w *walker) comments(node ast.Node, d protoreflect.Descriptor) string {
	text := w.res.SourceLocations().ByDescriptor(d).LeadingComments
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if cmts := w.tree.NodeInfo(node).LeadingComments(); cmts.Len() > 0 {
		raw := cmts.Index(cmts.Len() - 1).RawText()
		if strings.HasPrefix(raw, "/**") && len(raw) > len("/**/") {
			lines[0] = strings.TrimPrefix(lines[0], "*")
		}
		if strings.HasPrefix(raw, "/*") {
			// The one space before the closing marker, past a `**/`'s
			// star.
			last := len(lines) - 1
			if strings.HasSuffix(raw, "**/") && len(raw) > len("/**/") {
				lines[last] = strings.TrimSuffix(lines[last], "*")
			}
			lines[last] = strings.TrimSuffix(lines[last], " ")
		}
	}
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, " ")
	}
	blank := func(line string) bool { return strings.TrimSpace(line) == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// options is the options message of the name among
// google.protobuf's: the build's own descriptor.proto where the file
// sees it, else the toolchain's, as the compiler interprets the
// built-in options of a file that imports none.
func (w *walker) options(name protoreflect.Name) protoreflect.MessageDescriptor {
	full := protoreflect.FullName("google.protobuf").Append(name)
	if md, ok := w.find(full).(protoreflect.MessageDescriptor); ok {
		return md
	}
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(full)
	if err != nil {
		return nil
	}
	md, _ := d.(protoreflect.MessageDescriptor)
	return md
}

func (w *walker) message(md protoreflect.MessageDescriptor, mp *descriptorpb.DescriptorProto) {
	if md.IsMapEntry() {
		return
	}
	// A message's own options resolve in the enclosing scope, as the
	// compiler resolves them; what the message holds resolves in its
	// own.
	enclosing := md.Parent().FullName()
	var node, name ast.Node
	var body []ast.MessageElement
	switch n := w.res.MessageNode(mp).(type) {
	case *ast.MessageNode:
		if n == nil {
			return
		}
		node, name, body = n, n.Name, n.Decls
	case *ast.SyntheticGroupMessageNode:
		if n == nil {
			return
		}
		// The group's name token declares its field and its message
		// both; the field's span, recorded first by the enclosing
		// message's walk, is the one under a cursor, and the message's
		// is where its references lead.
		node, name, body = (*ast.GroupNode)(n), n.Name, (*ast.GroupNode)(n).Decls
	}
	w.declare(md, node, name, "message")
	for _, d := range body {
		if o, ok := d.(*ast.OptionNode); ok {
			w.option(o, w.options("MessageOptions"), enclosing, nil)
		}
	}
	for i, er := range mp.ExtensionRange {
		if er.Options == nil {
			continue
		}
		if n := w.res.ExtensionsNode(mp.ExtensionRange[i]); n != nil {
			n.RangeOptions(func(o *ast.OptionNode) bool {
				w.option(o, w.options("ExtensionRangeOptions"), md.FullName(), nil)
				return true
			})
		}
	}
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		w.field(fields.Get(i), mp.Field[i])
	}
	oneofs := md.Oneofs()
	for i := 0; i < oneofs.Len(); i++ {
		od := oneofs.Get(i)
		if n, ok := w.res.OneofNode(mp.OneofDecl[i]).(*ast.OneofNode); ok && n != nil {
			w.declare(od, n, n.Name, "oneof")
			n.RangeOptions(func(o *ast.OptionNode) bool {
				w.option(o, w.options("OneofOptions"), md.FullName(), nil)
				return true
			})
		}
	}
	nested := md.Messages()
	for i := 0; i < nested.Len(); i++ {
		w.message(nested.Get(i), mp.NestedType[i])
	}
	enums := md.Enums()
	for i := 0; i < enums.Len(); i++ {
		w.enum(enums.Get(i), mp.EnumType[i])
	}
	exts := md.Extensions()
	for i := 0; i < exts.Len(); i++ {
		w.field(exts.Get(i), mp.Extension[i])
	}
}

func (w *walker) field(fd protoreflect.FieldDescriptor, fp *descriptorpb.FieldDescriptorProto) {
	kind := "field"
	if fd.IsExtension() {
		kind = "extension"
	}
	var node, name, typ ast.Node
	var extendee *ast.ExtendNode
	var options *ast.CompactOptionsNode
	switch n := w.res.FieldNode(fp).(type) {
	case *ast.FieldNode:
		if n == nil {
			return
		}
		node, name, typ, extendee, options = n, n.Name, n.FldType, n.Extendee, n.Options
	case *ast.GroupNode:
		if n == nil {
			return
		}
		// The group's name token declares the field, whose name is the
		// group's lowercased.
		node, name, extendee, options, kind = n, n.Name, n.Extendee, n.Options, "group"
	case *ast.MapFieldNode:
		if n == nil {
			return
		}
		node, name, options = n, n.Name, n.Options
		// The map's value type names a message or an enum; the key is
		// scalar.
		if entry := fd.Message(); entry != nil {
			if vf := entry.Fields().ByName("value"); vf != nil {
				w.typeRef(n.MapType.ValueType, vf)
			}
		}
	default:
		return
	}
	w.declare(fd, node, name, kind)
	w.typeRef(typ, fd)
	if extendee != nil && !w.extended[extendee] {
		w.extended[extendee] = true
		w.refer(extendee.Extendee, fd.ContainingMessage())
	}
	// The compact options, read directly: the node's own range over
	// them dereferences an absent list. They resolve in the field's
	// enclosing scope.
	if options != nil {
		for _, o := range options.Options {
			w.option(o, w.options("FieldOptions"), fd.Parent().FullName(), fd)
		}
	}
}

// typeRef records a field's type token where it names a message or
// an enum.
func (w *walker) typeRef(typ ast.Node, fd protoreflect.FieldDescriptor) {
	if typ == nil {
		return
	}
	switch {
	case fd.Message() != nil:
		w.refer(typ, fd.Message())
	case fd.Enum() != nil:
		w.refer(typ, fd.Enum())
	}
}

func (w *walker) enum(ed protoreflect.EnumDescriptor, ep *descriptorpb.EnumDescriptorProto) {
	n, ok := w.res.EnumNode(ep).(*ast.EnumNode)
	if !ok || n == nil {
		return
	}
	enclosing := ed.Parent().FullName()
	w.declare(ed, n, n.Name, "enum")
	n.RangeOptions(func(o *ast.OptionNode) bool {
		w.option(o, w.options("EnumOptions"), enclosing, nil)
		return true
	})
	values := ed.Values()
	for i := 0; i < values.Len(); i++ {
		vd := values.Get(i)
		if vn, ok := w.res.EnumValueNode(ep.Value[i]).(*ast.EnumValueNode); ok && vn != nil {
			w.declare(vd, vn, vn.Name, "enum value")
			if vn.Options != nil {
				for _, o := range vn.Options.Options {
					w.option(o, w.options("EnumValueOptions"), enclosing, nil)
				}
			}
		}
	}
}

func (w *walker) service(sd protoreflect.ServiceDescriptor, sp *descriptorpb.ServiceDescriptorProto) {
	n, ok := w.res.ServiceNode(sp).(*ast.ServiceNode)
	if !ok || n == nil {
		return
	}
	w.declare(sd, n, n.Name, "service")
	n.RangeOptions(func(o *ast.OptionNode) bool {
		w.option(o, w.options("ServiceOptions"), sd.Parent().FullName(), nil)
		return true
	})
	methods := sd.Methods()
	for i := 0; i < methods.Len(); i++ {
		md := methods.Get(i)
		rn, ok := w.res.MethodNode(sp.Method[i]).(*ast.RPCNode)
		if !ok || rn == nil {
			continue
		}
		w.declare(md, rn, rn.Name, "rpc")
		if rn.Input != nil {
			w.refer(rn.Input.MessageType, md.Input())
		}
		if rn.Output != nil {
			w.refer(rn.Output.MessageType, md.Output())
		}
		rn.RangeOptions(func(o *ast.OptionNode) bool {
			w.option(o, w.options("MethodOptions"), sd.FullName(), nil)
			return true
		})
	}
}

// option records what an option's name parts and value bind: a plain
// part a field of the options message of the context, an extension
// part the extension it names, resolved from the given scope outward
// as the compiler resolves it; a value that is an enum's identifier
// the value, a message literal its fields in turn. A field's
// `default` is no option field: its value is typed by the field
// itself.
func (w *walker) option(o *ast.OptionNode, options protoreflect.MessageDescriptor, scope protoreflect.FullName, field protoreflect.Descriptor) {
	if o.Name == nil {
		return
	}
	if fd, ok := field.(protoreflect.FieldDescriptor); ok && len(o.Name.Parts) == 1 && !o.Name.Parts[0].IsExtension() && o.Name.Parts[0].Name.AsIdentifier() == "default" {
		w.value(o.Val, fd, scope)
		return
	}
	var fd protoreflect.FieldDescriptor
	msg := options
	for _, part := range o.Name.Parts {
		if msg == nil {
			return
		}
		if part.IsExtension() {
			ext := w.lookup(string(part.Name.AsIdentifier()), scope)
			if ext == nil {
				return
			}
			fd, _ = ext.(protoreflect.FieldDescriptor)
		} else {
			fd = msg.Fields().ByName(protoreflect.Name(part.Name.AsIdentifier()))
		}
		if fd == nil {
			return
		}
		w.refer(part.Name, fd)
		msg = fd.Message()
	}
	w.value(o.Val, fd, scope)
}

// value records what an option's value binds, typed by the field it
// is set on.
func (w *walker) value(v ast.ValueNode, fd protoreflect.FieldDescriptor, scope protoreflect.FullName) {
	if v == nil || fd == nil {
		return
	}
	switch n := v.(type) {
	case ast.IdentValueNode:
		if ed := fd.Enum(); ed != nil {
			if vd := ed.Values().ByName(protoreflect.Name(n.AsIdentifier())); vd != nil {
				w.refer(n, vd)
			}
		}
	case *ast.ArrayLiteralNode:
		for _, e := range n.Elements {
			w.value(e, fd, scope)
		}
	case *ast.MessageLiteralNode:
		if md := fd.Message(); md != nil {
			w.literal(n, md, scope)
		}
	}
}

// literal records what a message literal's fields bind, typed by md:
// a plain name the message's field — a group's by its type name, as
// the text format spells it — an extension name the extension the
// linker resolved, an Any type reference the message, whose literal
// is then typed by it; each value in turn.
func (w *walker) literal(lit *ast.MessageLiteralNode, md protoreflect.MessageDescriptor, scope protoreflect.FullName) {
	for _, e := range lit.Elements {
		if e.Name == nil {
			continue
		}
		var ef protoreflect.FieldDescriptor
		switch {
		case e.Name.IsAnyTypeReference():
			d, _ := w.lookup(string(e.Name.Name.AsIdentifier()), "").(protoreflect.MessageDescriptor)
			if d == nil {
				continue
			}
			w.refer(e.Name.Name, d)
			if inner, ok := e.Val.(*ast.MessageLiteralNode); ok {
				w.literal(inner, d, scope)
			}
			continue
		case e.Name.IsExtension():
			if d := w.lookup(w.res.ResolveMessageLiteralExtensionName(e.Name.Name), ""); d != nil {
				ef, _ = d.(protoreflect.FieldDescriptor)
			}
		default:
			name := protoreflect.Name(e.Name.Name.AsIdentifier())
			ef = md.Fields().ByName(name)
			if ef == nil {
				fields := md.Fields()
				for i := 0; i < fields.Len() && ef == nil; i++ {
					if f := fields.Get(i); f.Kind() == protoreflect.GroupKind && f.Message() != nil && f.Message().Name() == name {
						ef = f
					}
				}
			}
		}
		if ef == nil {
			continue
		}
		w.refer(e.Name.Name, ef)
		w.value(e.Val, ef, scope)
	}
}

// lookup resolves a name as written from a scope outward, as the
// compiler scopes names: the first enclosing scope under which the
// name exists, the root last; a fully-qualified name (a leading dot)
// at the root alone.
func (w *walker) lookup(name string, scope protoreflect.FullName) protoreflect.Descriptor {
	if strings.HasPrefix(name, ".") {
		return w.find(protoreflect.FullName(name[1:]))
	}
	for s := scope; s != ""; s = s.Parent() {
		if d := w.find(protoreflect.FullName(string(s) + "." + name)); d != nil {
			return d
		}
	}
	return w.find(protoreflect.FullName(name))
}

// find is a descriptor by full name across the file's closure.
func (w *walker) find(name protoreflect.FullName) protoreflect.Descriptor {
	d, err := w.resolver.FindDescriptorByName(name)
	if err != nil {
		return nil
	}
	return d
}

// locate is the server's navigation entry: the index of the last
// compiling build and the span under the position in the document,
// where the document is an open build file whose contents are those
// the build read; otherwise nothing (REQ-lsp-definition's rule). The
// index read once here is what the request answers from, with the
// document's text, which the span's offsets index.
func (s *Server) locate(docURI uri.URI, pos protocol.Position) (*index, *span, []byte, bool) {
	s.mu.Lock()
	doc := s.docs[docURI]
	standing := s.standing[docURI]
	idx := s.index
	enc := s.enc
	s.mu.Unlock()
	if doc == nil || !doc.proto || !standing || idx == nil {
		return nil, nil, nil, false
	}
	tree, ok := s.treePath(docURI)
	if !ok || tree == "" {
		return nil, nil, nil, false
	}
	// The document's file is the one the index holds at its tree path.
	p, ok := idx.build.byTree[tree]
	fi := idx.spans[p]
	if !ok || fi == nil || string(idx.build.byPath[p].text) != string(doc.text) {
		return nil, nil, nil, false
	}
	sp := fi.at(offsetAt(doc.text, pos, enc))
	if sp == nil {
		return nil, nil, nil, false
	}
	return idx, sp, doc.text, true
}

// location is a span's place as the protocol has it: the file's
// address by its origin (REQ-lsp-dependency-files) and the token's
// range under the encoding.
func (s *Server) location(idx *index, path string, start, end int) protocol.Location {
	s.mu.Lock()
	enc := s.enc
	s.mu.Unlock()
	bf := idx.build.byPath[path]
	return protocol.Location{URI: s.address(bf.origin, path), Range: rangeAt(bf.text, start, end, enc)}
}

// Definition answers the declaration the name under the position
// binds to, as its name token's location; an import's path, the
// imported file at its first line (REQ-lsp-definition).
func (s *Server) Definition(ctx context.Context, params *protocol.DefinitionParams) (protocol.DefinitionResult, error) {
	idx, sp, _, ok := s.locate(params.TextDocument.URI, params.Position)
	if !ok {
		return nil, nil
	}
	if sp.importPath != "" {
		if _, known := idx.spans[sp.importPath]; !known {
			return nil, nil
		}
		loc := s.location(idx, sp.importPath, 0, 0)
		return &loc, nil
	}
	d := idx.decls[sp.target]
	if d == nil {
		return nil, nil
	}
	loc := s.location(idx, d.path, d.start, d.end)
	return &loc, nil
}

// Hover answers the bound declaration's kind and full name in a
// fenced protobuf block, then its leading comments, the range the
// token under the position (REQ-lsp-hover).
func (s *Server) Hover(ctx context.Context, params *protocol.HoverParams) (*protocol.Hover, error) {
	idx, sp, text, ok := s.locate(params.TextDocument.URI, params.Position)
	if !ok || sp.target == "" {
		return nil, nil
	}
	d := idx.decls[sp.target]
	if d == nil {
		return nil, nil
	}
	value := fmt.Sprintf("```protobuf\n%s %s\n```", d.kind, sp.target)
	if d.comments != "" {
		value += "\n\n" + d.comments
	}
	s.mu.Lock()
	enc := s.enc
	s.mu.Unlock()
	r := rangeAt(text, sp.start, sp.end, enc)
	return &protocol.Hover{Contents: &protocol.MarkupContent{Kind: protocol.MarkupKindMarkdown, Value: value}, Range: &r}, nil
}

// References answers every reference to the bound declaration across
// the build's files, the declaration itself where asked, in URI then
// position order (REQ-lsp-references).
func (s *Server) References(ctx context.Context, params *protocol.ReferenceParams) ([]protocol.Location, error) {
	idx, sp, _, ok := s.locate(params.TextDocument.URI, params.Position)
	out := []protocol.Location{}
	if !ok || sp.target == "" {
		return out, nil
	}
	for _, fi := range idx.spans {
		for _, candidate := range fi.spans {
			if candidate.target != sp.target || (candidate.decl && !params.Context.IncludeDeclaration) {
				continue
			}
			out = append(out, s.location(idx, fi.path, candidate.start, candidate.end))
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].URI != out[b].URI {
			return out[a].URI < out[b].URI
		}
		if out[a].Range.Start.Line != out[b].Range.Start.Line {
			return out[a].Range.Start.Line < out[b].Range.Start.Line
		}
		return out[a].Range.Start.Character < out[b].Range.Start.Character
	})
	return out, nil
}

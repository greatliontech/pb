package format

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile/parser"
	"github.com/bufbuild/protocompile/reporter"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"pgregory.net/rapid"
)

var update = flag.Bool("update", false, "write the missing goldens of pb's own pairs from the current formatter")

// exceptions is the corpus pairs whose golden is no anchor, by the
// corpus's EXCEPTIONS file: each a golden losing a comment of its
// input, buf's own fault, pb's pair over the same input standing in.
func exceptions(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile("testdata/buf/EXCEPTIONS")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		p, reason, ok := strings.Cut(l, ": ")
		if !ok || reason == "" {
			t.Fatalf("EXCEPTIONS line %q", l)
		}
		out["testdata/buf/"+p] = true
	}
	return out
}

// pairs is every corpus pair under testdata: the .proto input and
// its .golden canonical form, by the input's path. buf's one pair
// without a golden, editions/2024, does not parse under
// protocompile (its `import option` form), which the walk pins.
func pairs(t *testing.T) map[string][2][]byte {
	t.Helper()
	out := map[string][2][]byte{}
	err := filepath.WalkDir("testdata", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".proto") {
			return err
		}
		p = filepath.ToSlash(p) // the corpus is keyed by slash paths on every platform
		golden := strings.TrimSuffix(p, ".proto") + ".golden"
		in, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		want, err := os.ReadFile(golden)
		if errors.Is(err, os.ErrNotExist) && *update && strings.HasPrefix(p, "testdata/pb/") {
			if want, err = Format(p, in); err != nil {
				return err
			}
			if err := os.WriteFile(golden, want, 0o644); err != nil {
				return err
			}
		} else if errors.Is(err, os.ErrNotExist) {
			if p != "testdata/buf/editions/2024/editions.proto" {
				return fmt.Errorf("%s: no golden", p)
			}
			if _, err := Format(p, in); !errors.Is(err, ErrParse) {
				return fmt.Errorf("%s: parsed, so it wants a golden: %v", p, err)
			}
			return nil
		}
		if err != nil {
			return err
		}
		out[p] = [2][]byte{in, want}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 50 {
		t.Fatalf("%d corpus pairs", len(out))
	}
	return out
}

// Every corpus pair formats to its golden byte for byte, every golden
// to itself, and every pair's two sides compile to one descriptor and
// carry the same comments (REQ-format-corpus, REQ-format-idempotent,
// REQ-format-tokens); a pair the corpus excepts is held to pb's pair
// over its input instead.
func TestCorpus(t *testing.T) {
	ps := pairs(t)
	names := make([]string, 0, len(ps))
	for p := range ps {
		names = append(names, p)
	}
	sort.Strings(names)
	excepted := exceptions(t)
	for _, p := range names {
		in, want := ps[p][0], ps[p][1]
		got, err := Format(p, in)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if excepted[p] {
			if _, ok := ps["testdata/pb/"+filepath.Base(p)]; !ok {
				t.Errorf("%s: excepted, but no pb pair over its input", p)
			}
			delete(excepted, p)
		} else {
			if !bytes.Equal(got, want) {
				t.Errorf("%s: formatted differs from the golden:\n%s", p, firstDifference(got, want))
			}
			again, err := Format(p, want)
			if err != nil || !bytes.Equal(again, want) {
				t.Errorf("%s: the golden is not a fixed point: %v\n%s", p, err, firstDifference(again, want))
			}
		}
		if a, b := descriptor(t, p, in), descriptor(t, p, got); !proto.Equal(a, b) {
			t.Errorf("%s: the formatted file compiles to another descriptor", p)
		}
		if a, b := commentTexts(in), commentTexts(got); !slices.Equal(a, b) {
			t.Errorf("%s: the comments differ:\n  in:  %q\n  out: %q", p, a, b)
		}
	}
	for p := range excepted {
		t.Errorf("EXCEPTIONS names %s, which is no corpus pair", p)
	}
}

// descriptor compiles a file's source to its descriptor without
// linking, source positions dropped, the header's order the canonical
// form's — imports and file options compared as sets, a public or
// weak import by name — and aggregate values compared by their
// tokens.
func descriptor(t *testing.T, name string, src []byte) *descriptorpb.FileDescriptorProto {
	t.Helper()
	handler := reporter.NewHandler(nil)
	file, err := parser.Parse(name, bytes.NewReader(src), handler)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	res, err := parser.ResultFromAST(file, false, handler)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	fd := proto.Clone(res.FileDescriptorProto()).(*descriptorpb.FileDescriptorProto)
	fd.SourceCodeInfo = nil
	for _, k := range fd.PublicDependency {
		fd.Dependency[k] = "public " + fd.Dependency[k]
	}
	for _, k := range fd.WeakDependency {
		fd.Dependency[k] = "weak " + fd.Dependency[k]
	}
	fd.PublicDependency, fd.WeakDependency = nil, nil
	sort.Strings(fd.Dependency)
	fd.Dependency = slices.Compact(fd.Dependency)
	if fd.Options != nil {
		opts := fd.Options.UninterpretedOption
		sort.Slice(opts, func(a, b int) bool { return prototext.Format(opts[a]) < prototext.Format(opts[b]) })
	}
	var walk func(m protoreflect.Message)
	walk = func(m protoreflect.Message) {
		m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			switch {
			case f.Name() == "aggregate_value" && f.Kind() == protoreflect.StringKind:
				m.Set(f, protoreflect.ValueOfString(canonicalAggregate(v.String())))
			case f.IsList() && f.Kind() == protoreflect.MessageKind:
				for k := range v.List().Len() {
					walk(v.List().Get(k).Message())
				}
			case f.Kind() == protoreflect.MessageKind && !f.IsMap():
				walk(v.Message())
			}
			return true
		})
	}
	walk(fd.ProtoReflect())
	return fd
}

// canonicalAggregate spells an aggregate value with its comments
// dropped, its whitespace, `,`, `;` and `:` outside strings each one
// space, runs of them collapsed, and `<` and `>` as braces, so the
// value compares by its tokens.
func canonicalAggregate(s string) string {
	var b strings.Builder
	scan(s, func(kind byte, text string) {
		switch kind {
		case 'c':
		case 'w':
			b.WriteByte(' ')
		case 's':
			b.WriteString(text)
		default:
			for _, r := range text {
				switch r {
				case ',', ';', ':':
					b.WriteByte(' ')
				case '<':
					b.WriteByte('{')
				case '>':
					b.WriteByte('}')
				default:
					b.WriteRune(r)
				}
			}
		}
	})
	return strings.Join(strings.Fields(b.String()), " ")
}

// commentTexts is every comment of a source, sorted, its text
// normalized as the canonical form may reflow it: the `//` or `/*
// */` marks dropped, each line trimmed, the lines joined by one
// space — so a comment lost or written twice shows, while the
// header's reordering and an in-line `//` comment's block form do
// not.
func commentTexts(src []byte) []string {
	var out []string
	scan(string(src), func(kind byte, text string) {
		if kind != 'c' {
			return
		}
		text = strings.TrimPrefix(text, "//")
		text = strings.TrimSuffix(strings.TrimPrefix(text, "/*"), "*/")
		lines := strings.Split(text, "\n")
		for k := range lines {
			lines[k] = strings.TrimSpace(lines[k])
		}
		out = append(out, strings.TrimSpace(strings.Join(lines, " ")))
	})
	sort.Strings(out)
	return out
}

// scan walks a source, telling each run apart: a comment ('c', its
// text, a `//` comment's without its newline), a string literal
// ('s'), a run of whitespace ('w') or anything else ('o').
func scan(s string, visit func(kind byte, text string)) {
	for k := 0; k < len(s); {
		c := s[k]
		switch {
		case c == '"' || c == '\'':
			q, from := c, k
			for k++; k < len(s); k++ {
				if s[k] == '\\' && k+1 < len(s) {
					k++
				} else if s[k] == q || s[k] == '\n' {
					break
				}
			}
			k++
			visit('s', s[from:min(k, len(s))])
		case c == '/' && k+1 < len(s) && s[k+1] == '/':
			from := k
			for k < len(s) && s[k] != '\n' {
				k++
			}
			visit('c', s[from:k])
		case c == '/' && k+1 < len(s) && s[k+1] == '*':
			end := strings.Index(s[k+2:], "*/")
			if end < 0 {
				visit('c', s[k:])
				return
			}
			visit('c', s[k:k+2+end+2])
			k += 2 + end + 2
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			from := k
			for k < len(s) && (s[k] == ' ' || s[k] == '\t' || s[k] == '\n' || s[k] == '\r') {
				k++
			}
			visit('w', s[from:k])
		default:
			from := k
			for k < len(s) && !strings.ContainsRune("\"'/ \t\n\r", rune(s[k])) {
				k++
			}
			if k == from {
				k++
			}
			visit('o', s[from:k])
		}
	}
}

// firstDifference shows the lines around the first differing line.
func firstDifference(got, want []byte) string {
	gl, wl := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
	for k := 0; k < len(gl) || k < len(wl); k++ {
		var g, w string
		if k < len(gl) {
			g = gl[k]
		}
		if k < len(wl) {
			w = wl[k]
		}
		if g != w {
			from := max(k-2, 0)
			return "line " + strconv.Itoa(k+1) + ":\n  got:  " + strings.Join(gl[from:min(k+3, len(gl))], "\n        ") + "\n  want: " + strings.Join(wl[from:min(k+3, len(wl))], "\n        ")
		}
	}
	return "lengths differ"
}

// A file that does not parse is refused naming it (REQ-format-verb).
func TestParseError(t *testing.T) {
	_, err := Format("x.proto", []byte("syntax = \"proto3\";\nmessage {"))
	if err == nil || !errors.Is(err, ErrParse) || !strings.Contains(err.Error(), "x.proto") {
		t.Fatalf("a file that does not parse: %v", err)
	}
}

// The canonical form is a pure function of the tokens and comments
// (REQ-format-pure): over every corpus input, the whitespace between
// tokens outside comments and strings redrawn at random with the
// blank-line facts kept, the form is the same; the form of the form
// is itself (REQ-format-idempotent); and the form compiles to the
// input's descriptor and carries its comments (REQ-format-tokens).
func TestPureProperty(t *testing.T) {
	ps := pairs(t)
	names := make([]string, 0, len(ps))
	for p := range ps {
		names = append(names, p)
	}
	sort.Strings(names)
	rapid.Check(t, func(rt *rapid.T) {
		name := rapid.SampledFrom(names).Draw(rt, "pair")
		in := ps[name][0]
		redrawn := redrawSpaces(rt, string(in))
		want, err := Format(name, in)
		if err != nil {
			rt.Fatal(err)
		}
		got, err := Format(name, []byte(redrawn))
		if err != nil {
			rt.Fatalf("%s redrawn: %v\n%s", name, err, redrawn)
		}
		if !bytes.Equal(got, want) {
			rt.Fatalf("%s redrawn formats otherwise:\n%s", name, firstDifference(got, want))
		}
		again, err := Format(name, got)
		if err != nil || !bytes.Equal(again, got) {
			rt.Fatalf("%s: not idempotent: %v\n%s", name, err, firstDifference(again, got))
		}
		if a, b := descriptor(t, name, []byte(redrawn)), descriptor(t, name, got); !proto.Equal(a, b) {
			rt.Fatalf("%s redrawn: the form compiles to another descriptor", name)
		}
		if a, b := commentTexts([]byte(redrawn)), commentTexts(got); !slices.Equal(a, b) {
			rt.Fatalf("%s redrawn: the comments differ:\n  in:  %q\n  out: %q", name, a, b)
		}
	})
}

// redrawSpaces replaces each run of whitespace outside comments and
// string literals by a random run with the same blank-line fact: as
// many newlines where fewer than two, two or more otherwise, spaces
// and tabs at random around them.
func redrawSpaces(rt *rapid.T, s string) string {
	var b strings.Builder
	run := rapid.SampledFrom([]string{"", " ", "  ", "\t", " \t "})
	scan(s, func(kind byte, text string) {
		if kind != 'w' {
			b.WriteString(text)
			return
		}
		newlines := strings.Count(text, "\n")
		if newlines > 1 {
			newlines = rapid.IntRange(2, 4).Draw(rt, "blank lines")
		}
		if newlines == 0 {
			b.WriteString(rapid.SampledFrom([]string{" ", "  ", "\t", " \t "}).Draw(rt, "run"))
			return
		}
		for range newlines {
			b.WriteString(run.Draw(rt, "before"))
			b.WriteString("\n")
		}
		b.WriteString(run.Draw(rt, "after"))
	})
	return b.String()
}

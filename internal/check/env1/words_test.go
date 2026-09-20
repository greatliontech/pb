package env1

import (
	"strings"
	"testing"
	"unicode"

	"pgregory.net/rapid"
)

// The segmentation term's examples and the boundaries it states.
func TestWords(t *testing.T) {
	cases := map[string][]string{
		"fooBar":               {"foo", "Bar"},
		"HTTPServer":           {"HTTP", "Server"},
		"Foo2Bar":              {"Foo2", "Bar"},
		"v1beta1":              {"v1beta1"},
		"foo_bar__baz":         {"foo", "bar", "baz"},
		"a.b.c":                {"a", "b", "c"},
		"getHTTPResponse2Code": {"get", "HTTP", "Response2", "Code"},
		"ID2":                  {"ID2"},
		"A0A":                  {"A0A"},
		"FOO_V2X":              {"FOO", "V2X"},
		"Foo2BAR":              {"Foo2", "BAR"},
		"V2x":                  {"V2x"},
		"HTTP2Server":          {"HTTP2", "Server"},
		"ABc":                  {"A", "Bc"},
		"AB":                   {"AB"},
		"__":                   {},
		"":                     {},
		"x":                    {"x"},
		"FOO_BAR":              {"FOO", "BAR"},
		"foo9bar":              {"foo9bar"},
		"9Lives":               {"9", "Lives"},
		"ÜberCafé":             {"Über", "Café"},
	}
	for in, want := range cases {
		got := Words(in)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("Words(%q) = %q, want %q", in, got, want)
		}
	}
	for word, want := range map[string]bool{"HTTP": true, "ID2": true, "A": false, "Ab": false, "2": false, "AB": true, "": false} {
		if Acronym(word) != want {
			t.Errorf("Acronym(%q) = %v", word, !want)
		}
	}
}

// The case function's examples: a name is in a style exactly when the
// function returns it unchanged, an acronym kept whole in pascal and
// camel, the first word lower-cased whole in camel.
func TestCase(t *testing.T) {
	cases := []struct{ in, style, want string }{
		{"get_http_response", StylePascal, "GetHttpResponse"},
		{"getHTTPResponse", StylePascal, "GetHTTPResponse"},
		{"HTTPServer", StyleCamel, "httpServer"},
		{"GetHTTPResponse", StyleCamel, "getHTTPResponse"},
		{"fooBar", StyleUpperSnake, "FOO_BAR"},
		{"FooBar", StyleSnake, "foo_bar"},
		{"HTTPServer", StyleSnake, "http_server"},
		{"foo_bar", StyleSnake, "foo_bar"},
		{"FOO_BAR", StyleUpperSnake, "FOO_BAR"},
		{"FooBar", StylePascal, "FooBar"},
		{"fooBar", StyleCamel, "fooBar"},
		{"Foo2Bar", StyleSnake, "foo2_bar"},
		{"FOO_V2X", StyleUpperSnake, "FOO_V2X"},
		{"Foo2BAR", StylePascal, "Foo2BAR"},
		{"v1beta1", StylePascal, "V1beta1"},
		{"", StylePascal, ""},
		{"__", StyleSnake, ""},
	}
	for _, c := range cases {
		got, ok := Case(c.in, c.style)
		if !ok || got != c.want {
			t.Errorf("Case(%q, %s) = %q, %v; want %q", c.in, c.style, got, ok, c.want)
		}
	}
	if _, ok := Case("x", "kebab"); ok {
		t.Error("kebab is a style")
	}
}

// Properties of segmentation and the case function: words are
// non-empty runs of letters and digits; a name built from lower-case
// words in a style is a fixed point of that style and segments back
// to those words.
func TestCaseProperties(t *testing.T) {
	// A word opens with two letters: a one-letter word is ambiguous in
	// pascal and camel (AA0a is A+A0a or AA0a), which is the styles'
	// nature, not the function's.
	word := rapid.Custom(func(t *rapid.T) string {
		head := rapid.StringMatching(`[a-z]{2}`).Draw(t, "head")
		tail := rapid.StringMatching(`[a-z0-9]{0,5}`).Draw(t, "tail")
		return head + tail
	})
	words := rapid.SliceOfN(word, 1, 5)
	rapid.Check(t, func(t *rapid.T) {
		ws := words.Draw(t, "words")
		snake := strings.Join(ws, "_")
		upper := strings.ToUpper(snake)
		var pascal, camel strings.Builder
		for i, w := range ws {
			cap := strings.ToUpper(w[:1]) + w[1:]
			pascal.WriteString(cap)
			if i == 0 {
				camel.WriteString(w)
			} else {
				camel.WriteString(cap)
			}
		}
		for style, name := range map[string]string{StyleSnake: snake, StyleUpperSnake: upper, StylePascal: pascal.String(), StyleCamel: camel.String()} {
			if got, _ := Case(name, style); got != name {
				t.Fatalf("%s %q is not a fixed point: %q", style, name, got)
			}
		}
		// Words segment back, case-insensitively: the snake and pascal
		// spellings both segment to the words.
		for _, name := range []string{snake, upper, pascal.String(), camel.String()} {
			got := Words(name)
			if len(got) != len(ws) {
				t.Fatalf("Words(%q) = %q, want %d words", name, got, len(ws))
			}
			for i := range ws {
				if !strings.EqualFold(got[i], ws[i]) {
					t.Fatalf("Words(%q)[%d] = %q, want %q", name, i, got[i], ws[i])
				}
			}
		}
	})
	rapid.Check(t, func(t *rapid.T) {
		name := rapid.String().Draw(t, "name")
		for _, w := range Words(name) {
			if w == "" {
				t.Fatal("an empty word")
			}
			for _, r := range w {
				if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
					t.Fatalf("word %q holds %q", w, r)
				}
			}
		}
	})
}

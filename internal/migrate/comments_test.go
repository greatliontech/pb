package migrate

import (
	"fmt"
	"testing"
)

// Suppression comments are rewritten as buf read them: a lint
// directive on any line of the block leading a line of code, the
// file otherwise byte-for-byte; a directive not on the block's last
// line reported displaced, one in a block comment reported as such;
// a directive buf never honored — trailing code, parted by a blank
// line or the file's end, breaking's — left and counted
// (REQ-migrate-comments).
func TestRewriteComments(t *testing.T) {
	for name, c := range map[string]struct {
		in, out string
		want    string // "rewritten/displaced/block/unplaced/inert"
	}{
		"line before":          {"// buf:lint:ignore FIELD_LOWER_SNAKE_CASE\nstring f = 1;\n", "// pb:ignore FIELD_LOWER_SNAKE_CASE\nstring f = 1;\n", "1/[]/[]/[]/0"},
		"reason kept":          {"// buf:lint:ignore X legacy name\nstring f = 1;\n", "// pb:ignore X legacy name\nstring f = 1;\n", "1/[]/[]/[]/0"},
		"stacked":              {"// buf:lint:ignore A\n// a note\n// buf:lint:ignore B\nmessage m {}\n", "// pb:ignore A\n// a note\n// pb:ignore B\nmessage m {}\n", "2/[1]/[]/[]/0"},
		"indented":             {"message M {\n  // buf:lint:ignore A\n  string BadName = 1;\n}\n", "message M {\n  // pb:ignore A\n  string BadName = 1;\n}\n", "1/[]/[]/[]/0"},
		"tabs before":          {"//\tbuf:lint:ignore X\nm;\n", "//\tpb:ignore X\nm;\n", "1/[]/[]/[]/0"},
		"no space":             {"//buf:lint:ignore X\nm\n", "//pb:ignore X\nm\n", "1/[]/[]/[]/0"},
		"trailing":             {"string BadName = 1; // buf:lint:ignore X\n", "string BadName = 1; // buf:lint:ignore X\n", "0/[]/[]/[]/1"},
		"breaking":             {"// buf:breaking:ignore FIELD_SAME_TYPE\nstring f = 1;\n", "// buf:breaking:ignore FIELD_SAME_TYPE\nstring f = 1;\n", "0/[]/[]/[]/1"},
		"detached":             {"// buf:lint:ignore A\n\nmessage m {}\n", "// buf:lint:ignore A\n\nmessage m {}\n", "0/[]/[]/[]/1"},
		"file's end":           {"m\n// buf:lint:ignore X", "m\n// buf:lint:ignore X", "0/[]/[]/[]/1"},
		"block comment":        {"/* buf:lint:ignore X */\nmessage m {}\n", "/* buf:lint:ignore X */\nmessage m {}\n", "0/[]/[1]/[]/0"},
		"block, lines":         {"/*\n  buf:lint:ignore X\n*/\nmessage m {}\n", "/*\n  buf:lint:ignore X\n*/\nmessage m {}\n", "0/[]/[2]/[]/0"},
		"block then line":      {"/* buf:lint:ignore X\n// buf:lint:ignore Y */ // buf:lint:ignore Z\nm\n", "/* buf:lint:ignore X\n// buf:lint:ignore Y */ // pb:ignore Z\nm\n", "1/[]/[1]/[]/0"},
		"unterminated blck":    {"/* buf:lint:ignore X\n// buf:lint:ignore Y\n", "/* buf:lint:ignore X\n// buf:lint:ignore Y\n", "0/[]/[]/[]/1"},
		"no id":                {"// buf:lint:ignore\nm\n// buf:lint:ignore \nm\n", "// buf:lint:ignore\nm\n// buf:lint:ignore \nm\n", "0/[]/[]/[]/0"},
		"other words":          {"// buf:lint:ignored FIELD\n// see buf:lint:ignore X\nm\n", "// buf:lint:ignored FIELD\n// see buf:lint:ignore X\nm\n", "0/[]/[]/[]/0"},
		"in a string":          {"option (x) = \"// buf:lint:ignore X\"; // buf:lint:ignore Y\n", "option (x) = \"// buf:lint:ignore X\"; // buf:lint:ignore Y\n", "0/[]/[]/[]/1"},
		"escaped quote":        {"option (x) = \"a\\\" // buf:lint:ignore X\";\n", "option (x) = \"a\\\" // buf:lint:ignore X\";\n", "0/[]/[]/[]/0"},
		"single quotes":        {"option (x) = '// buf:lint:ignore X';\n", "option (x) = '// buf:lint:ignore X';\n", "0/[]/[]/[]/0"},
		"unterminated str":     {"a = \"x // buf:lint:ignore X;\n// buf:lint:ignore Y\nb = 1;\n", "a = \"x // buf:lint:ignore X;\n// pb:ignore Y\nb = 1;\n", "1/[]/[]/[]/0"},
		"crlf":                 {"// buf:lint:ignore PACKAGE_VERSION_SUFFIX\r\nsyntax = \"proto3\";\r\n", "// pb:ignore PACKAGE_VERSION_SUFFIX\r\nsyntax = \"proto3\";\r\n", "1/[]/[]/[]/0"},
		"two blocks":           {"// buf:lint:ignore A\nm;\n// buf:lint:ignore B\nn;\n", "// pb:ignore A\nm;\n// pb:ignore B\nn;\n", "2/[]/[]/[]/0"},
		"mixed inert":          {"// buf:breaking:ignore B\n// buf:lint:ignore A\nm\n", "// buf:breaking:ignore B\n// pb:ignore A\nm\n", "1/[]/[]/[]/1"},
		"empty":                {"", "", "0/[]/[]/[]/0"},
		"file rule, package":   {"syntax = \"proto3\";\n\n// buf:lint:ignore PACKAGE_VERSION_SUFFIX\npackage foo;\n", "syntax = \"proto3\";\n\n// pb:ignore PACKAGE_VERSION_SUFFIX\npackage foo;\n", "1/[3]/[]/[]/0"},
		"file rule, syntax":    {"// buf:lint:ignore PACKAGE_VERSION_SUFFIX\nsyntax = \"proto3\";\npackage foo;\n", "// pb:ignore PACKAGE_VERSION_SUFFIX\nsyntax = \"proto3\";\npackage foo;\n", "1/[]/[]/[]/0"},
		"file rule, import":    {"syntax = \"proto3\";\n// buf:lint:ignore IMPORT_NO_PUBLIC\nimport public \"a.proto\";\n", "syntax = \"proto3\";\n// pb:ignore IMPORT_NO_PUBLIC\nimport public \"a.proto\";\n", "1/[2]/[]/[]/0"},
		"option in a body":     {"enum E {\n  // buf:lint:ignore ENUM_NO_ALLOW_ALIAS\n  option allow_alias = true;\n}\n", "enum E {\n  // pb:ignore ENUM_NO_ALLOW_ALIAS\n  option allow_alias = true;\n}\n", "1/[2]/[]/[]/0"},
		"reserved":             {"message M {\n  // buf:lint:ignore FIELD_LOWER_SNAKE_CASE\n  reserved 2;\n}\n", "message M {\n  // pb:ignore FIELD_LOWER_SNAKE_CASE\n  reserved 2;\n}\n", "1/[2]/[]/[]/0"},
		"option, no space":     {"enum E {\n  // buf:lint:ignore ENUM_NO_ALLOW_ALIAS\n  option(allow_alias) = true;\n}\n", "enum E {\n  // pb:ignore ENUM_NO_ALLOW_ALIAS\n  option(allow_alias) = true;\n}\n", "1/[2]/[]/[]/0"},
		"message rule, syntax": {"// buf:lint:ignore MESSAGE_PASCAL_CASE\nsyntax = \"proto3\";\n", "// pb:ignore MESSAGE_PASCAL_CASE\nsyntax = \"proto3\";\n", "1/[1]/[]/[]/0"},
		"message rule, import": {"syntax = \"proto3\";\n// buf:lint:ignore MESSAGE_PASCAL_CASE\nimport \"a.proto\";\n", "syntax = \"proto3\";\n// pb:ignore MESSAGE_PASCAL_CASE\nimport \"a.proto\";\n", "1/[2]/[]/[]/0"},
		"optional field":       {"message M {\n  // buf:lint:ignore FIELD_LOWER_SNAKE_CASE\n  optional string BadName = 1;\n}\n", "message M {\n  // pb:ignore FIELD_LOWER_SNAKE_CASE\n  optional string BadName = 1;\n}\n", "1/[]/[]/[]/0"},
		"two spaces":           {"// buf:lint:ignore  X\nm\n", "// buf:lint:ignore  X\nm\n", "0/[]/[]/[]/1"},
		"tab before id":        {"// buf:lint:ignore\tX\nm\n", "// buf:lint:ignore\tX\nm\n", "0/[]/[]/[]/1"},
		"package rule":         {"// buf:lint:ignore PACKAGE_SAME_DIRECTORY\npackage a;\n", "// pb:ignore PACKAGE_SAME_DIRECTORY\npackage a;\n", "1/[]/[]/[1]/0"},
		"set rule stacked":     {"// buf:lint:ignore PACKAGE_NO_IMPORT_CYCLE\n// buf:lint:ignore PACKAGE_LOWER_SNAKE_CASE\npackage A;\n", "// pb:ignore PACKAGE_NO_IMPORT_CYCLE\n// pb:ignore PACKAGE_LOWER_SNAKE_CASE\npackage A;\n", "2/[1]/[]/[1]/0"},
		"continued":            {"rpc Foo(Req)\n  // buf:lint:ignore RPC_RESPONSE_STANDARD_NAME\n  returns (Res);\n", "rpc Foo(Req)\n  // pb:ignore RPC_RESPONSE_STANDARD_NAME\n  returns (Res);\n", "1/[2]/[]/[]/0"},
		"after a body":         {"message M {\n  // buf:lint:ignore A\n  string f = 1; // note\n  // buf:lint:ignore B\n  string g = 2;\n}\n", "message M {\n  // pb:ignore A\n  string f = 1; // note\n  // pb:ignore B\n  string g = 2;\n}\n", "2/[]/[]/[]/0"},
	} {
		r := RewriteComments([]byte(c.in))
		got := fmt.Sprintf("%d/%v/%v/%v/%d", r.Rewritten, r.Displaced, r.Block, r.Unplaced, r.Inert)
		if string(r.Text) != c.out || got != c.want {
			t.Errorf("%s: got %q %s, want %q %s", name, r.Text, got, c.out, c.want)
		}
	}
}

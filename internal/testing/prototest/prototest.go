// Package prototest compiles in-memory protobuf sources for tests:
// the sources by import path, the well-known imports resolved, source
// info kept, so a test holds a linked schema and the text it came
// from.
package prototest

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/linker"
	"github.com/bufbuild/protocompile/wellknownimports"
)

// Compile links every source, in path order, failing the test on a
// compile error.
func Compile(t testing.TB, sources map[string]string) linker.Files {
	t.Helper()
	c := protocompile.Compiler{
		Resolver:       wellknownimports.WithStandardImports(&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)}),
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	names := make([]string, 0, len(sources))
	for n := range sources {
		names = append(names, n)
	}
	sort.Strings(names)
	files, err := c.Compile(context.Background(), names...)
	if err != nil {
		t.Fatalf("compiling the fixture: %v", err)
	}
	return files
}

// Source reads a path's bytes from the sources.
func Source(sources map[string]string) func(path string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		src, ok := sources[path]
		if !ok {
			return nil, errors.New("no such file")
		}
		return []byte(src), nil
	}
}

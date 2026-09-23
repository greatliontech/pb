package dep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"google.golang.org/protobuf/proto"

	"github.com/greatliontech/pb/internal/atomicfile"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/proto/compile"
)

// Build is the build verb (build.md): the resolved build compiled as
// generation compiles it (REQ-build-compile), its descriptor set —
// every reachable file in topological order, each descriptor as a
// plugin request carries it before any entry's overrides
// (REQ-build-set) — serialized deterministically and written whole to
// file, a regular file there replaced (REQ-build-output), then one
// report line naming the count of files and the file as given
// (REQ-build-report).
//
// file is the output file as the session's working tree names it;
// symbolic links on the way are resolved here and the set is written
// at the resolved path, the one judged before the build is resolved
// so a refusal costs no fetch: its parent exists, it names no protobuf
// source, and no directory stands there. given is the file as the
// user spelled it, for the report.
func Build(ctx context.Context, s *Session, file, given string, out io.Writer) error {
	file, err := s.buildTarget(file)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	_, mods, err := s.Modules(ctx)
	if err != nil {
		return err
	}
	compiled, err := compile.Compile(ctx, mods)
	if err != nil {
		return err
	}
	set := compiled.DescriptorSet()
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(set)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if err := atomicfile.Write(s.WS, file, ".pb-build-", 0o644, data); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	fmt.Fprintf(out, "built %d file(s) to %s\n", len(set.File), given)
	return nil
}

// buildTarget judges the output file before anything is resolved
// (REQ-build-output): it names no protobuf source, which the next
// load would compile; the symbolic links on the way are resolved and
// its parent directory exists (outputPath, shared with the export);
// and no directory stands there — a regular file or a symbolic link
// is replaced by the move. The resolved path is returned: the one
// judged is the one written.
func (s *Session) buildTarget(file string) (string, error) {
	if module.IsProtoFile(file) {
		return "", fmt.Errorf("%s names a protobuf source, which the next load would compile", file)
	}
	resolved, err := s.outputPath(file)
	if err != nil {
		return "", err
	}
	fi, err := s.WS.Lstat(resolved)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return resolved, nil
	case err != nil:
		return "", err
	case fi.IsDir():
		return "", fmt.Errorf("%s is a directory", file)
	}
	return resolved, nil
}

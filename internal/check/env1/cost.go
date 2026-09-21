package env1

import (
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"google.golang.org/protobuf/proto"
)

// The cost limit: a base every rule may spend plus a share per
// declaration of the schema, so a rule linear in the schema always
// fits and a rule quadratic in a large schema does not
// (REQ-rules-bounded). A library call is charged at least the size of
// its result and of the collection it searches, so a lookup over the
// whole schema costs what the schema costs.
const (
	costBase     = 1_000_000
	costPerEntry = 10_000
)

func costLimit(size int) uint64 { return costBase + costPerEntry*uint64(size) }

// costs is the runtime cost of the library's calls; every other
// function keeps cel-go's own charge.
type costs struct{ env *Env }

func (c costs) CallCost(function, overloadID string, args []ref.Val, result ref.Val) *uint64 {
	if !c.env.charged[function] {
		return nil
	}
	cost := uint64(1)
	switch function {
	case "resolve", "fileByName":
		// A lookup by name searches the schema.
		cost += uint64(c.env.new.Size())
	case "features":
		// A resolution per language feature the environment knows.
		cost += uint64(len(c.env.featureExts))
	case "packageCycles", "messages", "enums", "extensions", "services", "references", "imports", "visible":
		// A walk over what it was given: every declaration of each file
		// it reaches, and the file's imports.
		for _, a := range args {
			cost += c.walked(a)
		}
	}
	cost += size(result)
	return &cost
}

// walked is what a walk over an argument searches: for a file, its
// declarations and imports; for a message, the declarations beneath
// it.
func (c costs) walked(v ref.Val) uint64 {
	var n uint64
	one := func(v ref.Val) {
		msg, ok := v.Value().(proto.Message)
		if !ok {
			return
		}
		en, err := c.env.lookup(msg)
		if err != nil {
			return
		}
		if en.isFile() {
			n += uint64(en.file.decls + en.file.fd.Imports().Len())
			return
		}
		n += uint64(en.decls)
	}
	if l, ok := v.(traits.Lister); ok {
		for it := l.Iterator(); it.HasNext() == types.True; {
			one(it.Next())
		}
		return n + size(v)
	}
	one(v)
	return n
}

// size is a value's size: a collection's length, one for anything
// else.
func size(v ref.Val) uint64 {
	if s, ok := v.(traits.Sizer); ok {
		if n, ok := s.Size().Value().(int64); ok && n > 0 {
			return uint64(n)
		}
	}
	return 1
}

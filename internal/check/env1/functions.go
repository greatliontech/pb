package env1

import (
	"fmt"
	"sort"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/check/rules"
)

// userFunction is one rule-file function compiled under the
// environment (REQ-rules-functions): its body's program, evaluated
// per call over the parameters bound, and the cost of the last call,
// which the tracker charges the call at — the body's cost, as a
// library function's is its own.
type userFunction struct {
	name     string // as the declaring file spells it
	overload string // the declaration's overload id, unique across scopes
	params   []string
	ptypes   []*cel.Type
	ret      *cel.Type
	prg      cel.Program
	lastCost uint64
}

// compiledScope is a rule file's scope compiled: the declarations a
// rule of the file compiles against — its own functions unqualified,
// its imports' as `<alias>.<name>`.
type compiledScope struct {
	opts []cel.EnvOption
	err  error
	busy bool // compiling: a scope reached again through itself
}

// reserved are the names a rule's bindings take, which no alias may
// spell: `<alias>.<name>` would then read as a member call on the
// binding.
var reserved = func() map[string]bool {
	m := map[string]bool{BindFile: true, BindFiles: true, BindPackage: true, BindOld: true, BindNew: true, BindOldFile: true, BindNewFile: true, BindOldFiles: true, BindNewFiles: true, BindOldPkg: true, BindNewPkg: true}
	for _, t := range check.Targets() {
		m[EntityBinding(t)] = true
	}
	return m
}()

// typeOf is the CEL type a type spelling names in environment 1
// (REQ-env1-types), refused outside the vocabulary.
func typeOf(t rules.Type) (*cel.Type, error) {
	switch t.Name {
	case "bool":
		return cel.BoolType, nil
	case "int":
		return cel.IntType, nil
	case "uint":
		return cel.UintType, nil
	case "double":
		return cel.DoubleType, nil
	case "string":
		return cel.StringType, nil
	case "bytes":
		return cel.BytesType, nil
	case "null":
		return cel.NullType, nil
	case "dyn":
		return cel.DynType, nil
	case "list":
		elem, err := typeOf(t.Args[0])
		if err != nil {
			return nil, err
		}
		return cel.ListType(elem), nil
	case "map":
		key, err := typeOf(t.Args[0])
		if err != nil {
			return nil, err
		}
		switch key.Kind() {
		case cel.BoolKind, cel.IntKind, cel.UintKind, cel.StringKind, cel.DynKind:
		default:
			return nil, fmt.Errorf("type %s: a map's key is bool, int, uint, string or dyn, not %s", t, t.Args[0])
		}
		val, err := typeOf(t.Args[1])
		if err != nil {
			return nil, err
		}
		return cel.MapType(key, val), nil
	}
	for _, d := range descTypes {
		if d.TypeName() == t.Name {
			// A descriptor type admits null wherever a function declares
			// it, as the library's do: an absent side of a breaking pair
			// passes through, and a wrapper of the library yields what
			// the library yields.
			return types.NewNullableType(d), nil
		}
	}
	return nil, fmt.Errorf("type %s is none of environment 1's", t)
}

// scope compiles a rule file's scope once: its own functions in an
// order their calls admit — a function calling itself, directly or
// through another of the file, refused naming the cycle — each body
// type-checked against its parameters and refused unless it yields
// its declared return type, then declared for the file's rules; an
// import's functions compiled in their own file's scope and declared
// under the alias (REQ-rules-functions, REQ-rules-imports).
func (e *Env) scope(sc *rules.Scope) ([]cel.EnvOption, error) {
	if sc == nil {
		return nil, nil
	}
	if c, done := e.scopes[sc]; done {
		if c.busy {
			// The loader refuses import cycles; a scope reaching itself
			// is a fault of the caller's, named rather than followed.
			return nil, fmt.Errorf("%w: %s: compiles through itself", ErrCompile, sc.Where)
		}
		return c.opts, c.err
	}
	c := &compiledScope{busy: true}
	e.scopes[sc] = c
	c.opts, c.err = e.compileScope(sc)
	c.busy = false
	return c.opts, c.err
}

func (e *Env) compileScope(sc *rules.Scope) ([]cel.EnvOption, error) {
	var opts []cel.EnvOption
	// The imports' functions first: what the file's own may call. Each
	// lent function is compiled through its own file's scope — in that
	// file's call order, under its own imports — then declared here.
	for _, imp := range sc.Imports {
		if reserved[imp.Alias] {
			return nil, fmt.Errorf("%w: %s: import alias %s is a binding's name", ErrCompile, sc.Where, imp.Alias)
		}
	}
	aliases := make([]string, 0, len(sc.Lent))
	for a := range sc.Lent {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	var lent []cel.EnvOption
	for _, alias := range aliases {
		for _, l := range sc.Lent[alias] {
			if _, err := e.scope(l.Scope); err != nil {
				return nil, err
			}
			uf := e.functions[functionKey{l.Scope, l.Function.Name}]
			lent = append(lent, e.declare(alias+"."+l.Function.Name, uf))
		}
	}
	opts = append(opts, lent...)
	// The file's own, in call order, each body seeing the imports'
	// declarations above.
	order, err := e.callOrder(sc)
	if err != nil {
		return nil, err
	}
	for _, f := range order {
		uf, err := e.function(sc, f, lent)
		if err != nil {
			return nil, err
		}
		opts = append(opts, e.declare(f.Name, uf))
	}
	return opts, nil
}

// function compiles one function of a scope once: its body under the
// environment with the parameters bound at their types and the
// functions the scope sees before it — the imports' declarations
// given, and the file's own declared earlier in call order.
func (e *Env) function(sc *rules.Scope, f rules.Function, lent []cel.EnvOption) (*userFunction, error) {
	key := functionKey{sc, f.Name}
	if uf, done := e.functions[key]; done {
		return uf, nil
	}
	if e.base.HasFunction(f.Name) || e.macros[f.Name] {
		return nil, fmt.Errorf("%w: %s: function %s shadows the environment's", ErrCompile, sc.Where, f.Name)
	}
	uf := &userFunction{name: f.Name, overload: fmt.Sprintf("%s.%s#%d", sc.Where, f.Name, len(e.functions))}
	var opts []cel.EnvOption
	for _, p := range f.Params {
		pt, err := typeOf(p.Type)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: function %s, parameter %s: %v", ErrCompile, sc.Where, f.Name, p.Name, err)
		}
		uf.params, uf.ptypes = append(uf.params, p.Name), append(uf.ptypes, pt)
		opts = append(opts, cel.Variable(p.Name, pt))
	}
	ret, err := typeOf(f.Returns)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: function %s returns: %v", ErrCompile, sc.Where, f.Name, err)
	}
	uf.ret = ret
	// What the body sees: the scope's imports and the functions of the
	// file compiled so far — call order compiles a callee first.
	opts = append(opts, lent...)
	for _, other := range e.seen(sc) {
		opts = append(opts, e.declare(other.name, other))
	}
	env, err := e.base.Extend(opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: function %s: %v", ErrCompile, sc.Where, f.Name, err)
	}
	ast, iss := env.Compile(f.CEL)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("%w: %s: function %s: %v", ErrCompile, sc.Where, f.Name, iss.Err())
	}
	// The declared type must admit the body's: an exact match, or a
	// body of no fixed type (dyn, or a list or map over dyn), which
	// each call holds to the declared type.
	if !admits(ret, ast.OutputType()) {
		return nil, fmt.Errorf("%w: %s: function %s: the body yields %s, not the declared %s", ErrCompile, sc.Where, f.Name, ast.OutputType(), f.Returns)
	}
	prg, err := env.Program(ast, cel.CostLimit(e.limit), cel.CostTracking(costs{e}), cel.EvalOptions(cel.OptOptimize))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: function %s: %v", ErrCompile, sc.Where, f.Name, err)
	}
	uf.prg = prg
	e.functions[key] = uf
	return uf, nil
}

// seen is the functions of a scope compiled so far, in the order they
// were.
func (e *Env) seen(sc *rules.Scope) []*userFunction {
	var out []*userFunction
	for _, f := range sc.Functions {
		if uf, done := e.functions[functionKey{sc, f.Name}]; done {
			out = append(out, uf)
		}
	}
	return out
}

// declare is the declaration of a compiled function under a name for
// a caller's environment: one overload at the declared types, its id
// the name's — one function declared under two aliases is two
// overloads — its binding the body evaluated over the arguments, the
// call's cost the body's (cost.go).
func (e *Env) declare(name string, uf *userFunction) cel.EnvOption {
	id := name + "#" + uf.overload
	e.byOverload[id] = uf
	return cel.Function(name, cel.Overload(id, uf.ptypes, uf.ret, cel.FunctionBinding(func(args ...ref.Val) ref.Val {
		vars := make(map[string]any, len(uf.params))
		for i, p := range uf.params {
			vars[p] = args[i]
		}
		out, details, err := uf.prg.Eval(vars)
		if details != nil && details.ActualCost() != nil {
			uf.lastCost = *details.ActualCost()
		}
		if err != nil {
			return types.NewErr("function %s: %v", uf.name, err)
		}
		if off := conforms(uf.ret, out); off != nil {
			if off.want == uf.ret {
				return types.NewErr("function %s: the body yielded %s, not the declared %s", uf.name, off.Type(), uf.ret)
			}
			return types.NewErr("function %s: the body yielded %s where the declared %s takes %s", uf.name, off.Type(), uf.ret, off.want)
		}
		return out
	})))
}

// admits reports whether a declared type takes a body's: the same
// type, or one of no fixed type (dyn) on either side, parameters
// judged alike — a list or map over dyn admitted by a typed one, each
// call holding the value to the declared type.
func admits(declared, got *cel.Type) bool {
	if declared.IsExactType(cel.DynType) || got.IsExactType(cel.DynType) {
		return true
	}
	if got.IsExactType(cel.NullType) && declared.IsAssignableType(cel.NullType) {
		return true
	}
	if declared.Kind() != got.Kind() || declared.TypeName() != got.TypeName() || len(declared.Parameters()) != len(got.Parameters()) {
		return false
	}
	for i, p := range declared.Parameters() {
		if !admits(p, got.Parameters()[i]) {
			return false
		}
	}
	return true
}

// offType is a value found off its declared type, and the type the
// declaration takes there.
type offType struct {
	ref.Val
	want *cel.Type
}

// conforms holds a value to a declared type, element by element for
// a list and entry by entry for a map — a runtime type erases the
// parameters — returning the first value off its type, nil where the
// whole conforms; dyn takes anything.
func conforms(t *cel.Type, v ref.Val) *offType {
	if t.IsExactType(cel.DynType) {
		return nil
	}
	// A list or map is judged by its kind and then every member: the
	// runtime's own judgement of a container samples one member,
	// whichever iteration yields first.
	switch t.Kind() {
	case cel.ListKind:
		l, ok := v.(traits.Lister)
		if !ok {
			return &offType{v, t}
		}
		for it := l.Iterator(); it.HasNext() == types.True; {
			if off := conforms(t.Parameters()[0], it.Next()); off != nil {
				return off
			}
		}
	case cel.MapKind:
		m, ok := v.(traits.Mapper)
		if !ok {
			return &offType{v, t}
		}
		for it := m.Iterator(); it.HasNext() == types.True; {
			k := it.Next()
			if off := conforms(t.Parameters()[0], k); off != nil {
				return off
			}
			if off := conforms(t.Parameters()[1], m.Get(k)); off != nil {
				return off
			}
		}
	default:
		if !t.IsAssignableRuntimeType(v) {
			return &offType{v, t}
		}
	}
	return nil
}

// functionKey names one function of one scope.
type functionKey struct {
	scope *rules.Scope
	name  string
}

// callOrder is the scope's own functions in an order that compiles a
// callee before its caller — the calls read from each body parsed,
// unqualified names of the scope's own alone — a cycle refused
// naming it (REQ-rules-functions).
func (e *Env) callOrder(sc *rules.Scope) ([]rules.Function, error) {
	own := map[string]rules.Function{}
	for _, f := range sc.Functions {
		own[f.Name] = f
	}
	calls := map[string][]string{}
	for _, f := range sc.Functions {
		parsed, iss := e.base.Parse(f.CEL)
		if iss != nil && iss.Err() != nil {
			return nil, fmt.Errorf("%w: %s: function %s: %v", ErrCompile, sc.Where, f.Name, iss.Err())
		}
		seen := map[string]bool{}
		ast.PreOrderVisit(parsed.NativeRep().Expr(), ast.NewExprVisitor(func(ex ast.Expr) {
			if ex.Kind() != ast.CallKind {
				return
			}
			name := ex.AsCall().FunctionName()
			if _, isOwn := own[name]; isOwn && !seen[name] && !ex.AsCall().IsMemberFunction() {
				seen[name] = true
				calls[f.Name] = append(calls[f.Name], name)
			}
		}))
	}
	// Depth first from each function in declaration order: a name on
	// the current path is a cycle, spelled from its first member.
	const (
		unvisited = iota
		onPath
		done
	)
	state := map[string]int{}
	var order []rules.Function
	var path []string
	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case done:
			return nil
		case onPath:
			start := 0
			for i, p := range path {
				if p == name {
					start = i
				}
			}
			cycle := append(append([]string(nil), path[start:]...), name)
			return fmt.Errorf("%w: %s: functions call in a cycle: %s", ErrCompile, sc.Where, strings.Join(cycle, " -> "))
		}
		state[name] = onPath
		path = append(path, name)
		for _, callee := range calls[name] {
			if err := visit(callee); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		state[name] = done
		order = append(order, own[name])
		return nil
	}
	for _, f := range sc.Functions {
		if err := visit(f.Name); err != nil {
			return nil, err
		}
	}
	return order, nil
}

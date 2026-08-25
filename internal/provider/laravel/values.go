package laravel

import (
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/php"
)

// routeScope is the php.Scope a route registration is evaluated in. It supplies
// three things the path expression can depend on:
//
//   - the bindings of the `foreach` loops the registration sits inside, so a
//     loop-registered block expands to one endpoint per iteration;
//   - the file's top-level variables, which is how the array a loop iterates
//     usually arrives (declared at the top of the route file, captured into a
//     group closure with `use ($x)`);
//   - `config('a.b')` / `env('X')`, resolved through the config layer.
//
// Everything else is unresolved, and an unresolved path is not emitted.
type routeScope struct {
	file  *php.File
	loops map[string][]string
	cfg   provider.ConfigResolver

	// visiting breaks reference cycles between top-level variables
	// (`$a = $b . 'x'; $b = $a;`), which are legal to write and would
	// otherwise recurse forever.
	visiting map[string]bool

	// sources records every config file consulted while evaluating one
	// expression, so a caller can report provenance. A value assembled from
	// more than one source has no single file to name honestly, and the
	// Dependency contract says to leave the field blank in that case.
	sources []string
}

// sourceUsed returns the single config source this scope consulted, or "" when
// it consulted none or several.
func (s *routeScope) sourceUsed() string {
	seen := ""
	for _, src := range s.sources {
		if src == "" {
			continue
		}
		if seen != "" && seen != src {
			return ""
		}
		seen = src
	}
	return seen
}

func newRouteScope(f *php.File, call php.Node, idx *provider.Index) *routeScope {
	sc := &routeScope{file: f, visiting: map[string]bool{}}
	if idx != nil {
		sc.cfg = idx.Config
	}
	sc.loops = loopBindings(f, call, sc)
	return sc
}

// Var resolves a variable: an enclosing loop's binding first, then the file's
// top-level assignments.
func (s *routeScope) Var(name string) ([]string, bool) {
	if v, ok := s.loops[name]; ok {
		return v, true
	}
	if s.visiting[name] {
		return nil, false
	}
	expr, ok := s.file.TopLevelVars()[name]
	if !ok {
		return nil, false
	}
	s.visiting[name] = true
	defer delete(s.visiting, name)
	return php.Eval(expr, s)
}

// Call resolves Laravel's two config accessors. `config('a.b')` reads the merged
// config/*.php tree; `env('X', 'fallback')` reads the environment layer and
// falls back to the literal default the code itself supplies — which is a real
// value the service runs with when the variable is unset.
func (s *routeScope) Call(call php.Node) ([]string, bool) {
	name := php.CallName(call)
	if name != "config" && name != "env" {
		return nil, false
	}
	key, ok := php.StringLit(php.PositionalArg(call, 0))
	if !ok {
		return nil, false
	}
	if s.cfg != nil {
		if v, _, src, ok := s.cfg.Resolve(key); ok {
			s.sources = append(s.sources, src)
			return []string{v}, true
		}
	}
	if name == "env" {
		if def, ok := php.StringLit(php.PositionalArg(call, 1)); ok {
			return []string{def}, true
		}
	}
	return nil, false
}

// loopBindings collects the bindings of every `foreach` enclosing a call.
// Ancestors are walked outward, and an inner loop shadows an outer one of the
// same name — the same way PHP itself would.
func loopBindings(f *php.File, call php.Node, sc *routeScope) map[string][]string {
	out := map[string][]string{}
	for n := call.Parent(); n.Valid(); n = n.Parent() {
		if n.Type() == "foreach_statement" {
			bindForeach(f, n, sc, out)
		}
	}
	return out
}

// bindForeach binds one `foreach ($arr as $k => $v)` / `foreach ($arr as $v)`
// over an array LITERAL. The collection must resolve to a literal array — either
// written inline or held by a top-level variable — because the point is to know
// the iteration set exactly. A collection built at runtime binds nothing, and
// the registration inside stays unemitted.
func bindForeach(f *php.File, fe php.Node, sc *routeScope, out map[string][]string) {
	kids := php.NamedChildren(fe)
	if len(kids) < 2 {
		return
	}
	arr := arrayLiteral(f, kids[0])
	if !arr.Valid() {
		return
	}
	entries := php.ArrayEntries(arr)
	if len(entries) == 0 {
		return
	}
	switch kids[1].Type() {
	case "pair": // $k => $v
		p := php.NamedChildren(kids[1])
		if len(p) != 2 {
			return
		}
		if keys, ok := entryKeys(entries); ok {
			bind(out, php.VarName(p[0]), keys)
		}
		if vals, ok := entryValues(entries, sc); ok {
			bind(out, php.VarName(p[1]), vals)
		}
	case "variable_name": // $v
		if vals, ok := entryValues(entries, sc); ok {
			bind(out, php.VarName(kids[1]), vals)
		}
	}
}

// bind records a binding unless the name already has one, so the innermost
// enclosing loop wins.
func bind(out map[string][]string, name string, values []string) {
	if name == "" {
		return
	}
	if _, taken := out[name]; !taken {
		out[name] = values
	}
}

// arrayLiteral returns the array literal an expression denotes: written inline,
// or held by a top-level variable.
func arrayLiteral(f *php.File, expr php.Node) php.Node {
	switch expr.Type() {
	case "array_creation_expression":
		return expr
	case "variable_name":
		if v, ok := f.TopLevelVars()[php.VarName(expr)]; ok && v.Type() == "array_creation_expression" {
			return v
		}
	}
	return php.Node{}
}

// entryKeys returns the array's string keys. EVERY entry must have one: a
// partial key list would silently register a subset of the real routes, which
// reads as a complete answer.
func entryKeys(entries []php.ArrayEntry) ([]string, bool) {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Key == "" {
			return nil, false
		}
		out = append(out, e.Key)
	}
	return out, true
}

// entryValues returns the array's values, every one of which must evaluate to a
// literal, for the same reason as entryKeys.
func entryValues(entries []php.ArrayEntry, sc *routeScope) ([]string, bool) {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		vals, ok := php.Eval(e.Value, sc)
		if !ok {
			return nil, false
		}
		out = append(out, vals...)
	}
	return out, true
}

package php

import "strings"

// Scope supplies the non-syntactic half of evaluation. Eval walks the expression
// tree; everything that depends on what the surrounding program DOES — what a
// variable holds, what a config accessor returns — comes from here, so the
// language layer stays free of framework knowledge.
//
// A nil Scope is valid and evaluates literal expressions only.
type Scope interface {
	// Var returns the literal values a variable (name without the leading '$')
	// can hold, or ok=false when that is not statically known.
	Var(name string) ([]string, bool)
	// Call returns the literal values a call evaluates to — Laravel's
	// `config('services.x.url')` and `env('X')` are the motivating cases — or
	// ok=false for a call the scope does not model.
	Call(call Node) ([]string, bool)
}

// maxEvalValues bounds the cross product. A path built from two loops over
// 30-entry arrays is 900 endpoints, which is a bug in the reading, not a
// service. Past the cap Eval reports the expression unresolved rather than
// emitting a truncated set that would look complete.
const maxEvalValues = 512

// Eval returns every literal string an expression can evaluate to, and whether
// it resolved COMPLETELY. Partial resolution is never reported as success: a
// caller emitting an endpoint needs the whole path, and half of one is not a
// weaker answer but a wrong one.
//
// Handled: string literals, interpolated strings ("{$a}/b"), `.` concatenation,
// variables and calls (via the Scope), and parenthesized forms. Anything else —
// a method call, a ternary, arithmetic — is unresolved.
func Eval(n Node, sc Scope) ([]string, bool) {
	if !n.Valid() {
		return nil, false
	}
	switch n.Type() {
	case "string":
		return []string{stringParts(n)}, true
	case "string_content", "escape_sequence":
		return []string{n.Text()}, true
	case "encapsed_string":
		// Children interleave literal segments and interpolated expressions in
		// source order, so the string IS the concatenation of its parts.
		return evalConcat(NamedChildren(n), sc)
	case "binary_expression":
		if n.ChildByFieldName("operator").Text() != "." {
			return nil, false // arithmetic/comparison: not a string
		}
		return evalConcat([]Node{n.ChildByFieldName("left"), n.ChildByFieldName("right")}, sc)
	case "parenthesized_expression":
		kids := NamedChildren(n)
		if len(kids) != 1 {
			return nil, false
		}
		return Eval(kids[0], sc)
	case "variable_name":
		if sc == nil {
			return nil, false
		}
		return sc.Var(VarName(n))
	}
	if sc != nil && IsCall(n) {
		return sc.Call(n)
	}
	return nil, false
}

// evalConcat evaluates parts left to right and returns their cross product, in a
// deterministic order (parts in source order, values in the order the Scope gave
// them). One unresolved part makes the whole expression unresolved.
func evalConcat(parts []Node, sc Scope) ([]string, bool) {
	out := []string{""}
	for _, p := range parts {
		vals, ok := Eval(p, sc)
		if !ok {
			return nil, false
		}
		if len(out)*len(vals) > maxEvalValues {
			return nil, false
		}
		next := make([]string, 0, len(out)*len(vals))
		for _, prefix := range out {
			for _, v := range vals {
				next = append(next, prefix+v)
			}
		}
		out = next
	}
	return out, true
}

// VarName returns a variable's name without the leading '$'.
func VarName(n Node) string {
	if n.Type() != "variable_name" {
		return ""
	}
	return ChildByType(n, "name").Text()
}

// TopLevelVars returns the file's top-level `$var = <expr>` assignments, keyed by
// variable name without the '$'.
//
// Only assignments at file scope are indexed, and only names assigned EXACTLY
// ONCE. Both restrictions are about not being wrong rather than being thorough:
// an assignment inside a function belongs to a call this analysis is not
// tracking, and a name assigned twice has no single value at the point of use.
// A closure that captures a file-scope array with `use ($x)` is the case this
// exists for, and it needs neither.
//
// Computed once per file and cached; the result is read-only.
func (f *File) TopLevelVars() map[string]Node {
	f.varsOnce.Do(func() {
		vars := map[string]Node{}
		seen := map[string]int{}
		root := f.Root()
		for _, stmt := range NamedChildren(root) {
			if stmt.Type() != "expression_statement" {
				continue
			}
			kids := NamedChildren(stmt)
			if len(kids) != 1 || kids[0].Type() != "assignment_expression" {
				continue
			}
			assign := kids[0]
			left := assign.ChildByFieldName("left")
			if left.Type() != "variable_name" {
				continue // $x['k'] = ... and friends: not a whole-value binding
			}
			name := VarName(left)
			seen[name]++
			vars[name] = assign.ChildByFieldName("right")
		}
		for name, n := range seen {
			if n > 1 {
				delete(vars, name)
			}
		}
		f.vars = vars
	})
	return f.vars
}

// Namespace returns the file's declared namespace, or "" for the global one.
func (f *File) Namespace() string {
	f.nsOnce.Do(func() {
		for _, n := range NamedChildren(f.Root()) {
			if n.Type() == "namespace_definition" {
				f.ns = ChildByType(n, "namespace_name").Text()
				return
			}
		}
	})
	return f.ns
}

// Imports returns the file's `use` statements as alias -> fully-qualified name.
//
// This is what makes a bare class name resolvable. A repo the size of a real
// Laravel app has several classes per simple name — laravel-blog declares THREE
// `PostController`s (web, admin, API v1) — and the `use` line at the top of the
// route file is what says which one a registration means. Resolving on the
// simple name alone picks one of the three and reads the wrong contract.
//
// Both spellings are handled: `use A\B\C;` and `use A\B\C as D;`, including the
// grouped `use A\B\{C, D};` form.
func (f *File) Imports() map[string]string {
	f.impOnce.Do(func() {
		imports := map[string]string{}
		for _, n := range NamedChildren(f.Root()) {
			if n.Type() != "namespace_use_declaration" {
				continue
			}
			collectUses(n, "", imports)
		}
		f.imports = imports
	})
	return f.imports
}

// collectUses walks one `use` declaration, which may hold several clauses and a
// group prefix.
func collectUses(n Node, prefix string, out map[string]string) {
	for _, c := range NamedChildren(n) {
		switch c.Type() {
		case "namespace_name":
			prefix = c.Text() // the group prefix of `use A\B\{...}`
		case "namespace_use_group":
			for _, g := range NamedChildren(c) {
				collectUses(g, prefix, out)
			}
		case "namespace_use_clause", "namespace_use_group_clause":
			addUseClause(c, prefix, out)
		}
	}
}

func addUseClause(c Node, prefix string, out map[string]string) {
	var fqn, alias string
	for _, k := range NamedChildren(c) {
		switch k.Type() {
		case "qualified_name", "namespace_name":
			fqn = strings.TrimPrefix(k.Text(), `\`)
		case "name":
			if fqn == "" {
				fqn = k.Text()
			}
		case "namespace_aliasing_clause":
			// `use A\B\Post as PostResource` — the alias is what the rest of the
			// file writes, and it is routinely NOT the class's own name.
			// laravel-blog imports `App\Http\Resources\Post as PostResource`,
			// where `Post` alone would collide with the Eloquent model.
			alias = ChildByType(k, "name").Text()
		}
	}
	if fqn == "" {
		return
	}
	if prefix != "" {
		fqn = strings.TrimSuffix(prefix, `\`) + `\` + fqn
	}
	if alias == "" {
		alias = fqn
		if i := strings.LastIndexByte(alias, '\\'); i >= 0 {
			alias = alias[i+1:]
		}
	}
	if _, taken := out[alias]; !taken {
		out[alias] = fqn
	}
}

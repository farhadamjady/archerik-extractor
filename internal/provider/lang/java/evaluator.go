package java

import (
	"regexp"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/resolve"
)

// maxEvalDepth bounds expression recursion (defensive; deep chains and variable
// following are the real risk, guarded further in PR 15).
const maxEvalDepth = 20

// Evaluator implements provider.Resolver for Java: it walks a target expression
// (a URL/topic argument) and returns the possible string values as a ValueSet.
// It resolves literals, string concatenation, constants (SymbolIndex),
// @Value-injected fields (ConfigResolver), and String.format; method params,
// local variables (until PR 15), and opaque calls become holes.
type Evaluator struct {
	symbols provider.SymbolIndex
	config  provider.ConfigResolver
}

// NewEvaluator binds an evaluator to the built Index.
func NewEvaluator(idx *provider.Index) *Evaluator {
	e := &Evaluator{}
	if idx != nil {
		e.symbols = idx.Symbols
		e.config = idx.Config
	}
	return e
}

// Resolve evaluates an AST expression node to a ValueSet.
func (e *Evaluator) Resolve(node provider.ASTNode) resolve.ValueSet {
	n, ok := node.(Node)
	if !ok || !n.Valid() {
		return resolve.NewUnknown()
	}
	c := &evalCtx{e: e, memo: map[uint32]resolve.ValueSet{}}
	return c.eval(n, 0)
}

// evalCtx holds per-Resolve state. memo is keyed by node start byte (unique
// within one file), so a value revisited via variable-following (PR 15) is
// computed once.
type evalCtx struct {
	e    *Evaluator
	memo map[uint32]resolve.ValueSet
}

func (c *evalCtx) eval(n Node, depth int) resolve.ValueSet {
	if !n.Valid() || depth > maxEvalDepth {
		return resolve.NewUnknown()
	}
	key := n.StartByte()
	if v, ok := c.memo[key]; ok {
		return v
	}
	r := c.evalNode(n, depth)
	c.memo[key] = r
	return r
}

func (c *evalCtx) evalNode(n Node, depth int) resolve.ValueSet {
	switch n.Type() {
	case "string_literal":
		return resolve.NewExact(model.Confirmed, stripQuotes(n.Text()))
	case "parenthesized_expression":
		return c.eval(n.NamedChild(0), depth+1)
	case "binary_expression":
		return c.evalBinary(n, depth)
	case "identifier":
		return c.evalName(n, n.Text())
	case "field_access":
		return c.evalFieldAccess(n)
	case "method_invocation":
		return c.evalMethodInvocation(n, depth)
	default:
		return resolve.NewUnknown()
	}
}

// evalBinary handles string concatenation (a + b); other operators are opaque.
func (c *evalCtx) evalBinary(n Node, depth int) resolve.ValueSet {
	if n.ChildByFieldName("operator").Text() != "+" {
		return resolve.NewUnknown()
	}
	left := c.eval(n.ChildByFieldName("left"), depth+1)
	right := c.eval(n.ChildByFieldName("right"), depth+1)
	return resolve.Concat(left, right)
}

// evalName resolves a bare reference: a @Value field in the enclosing class, or
// an unambiguous constant. Anything else (local var, param) is unknown for now.
func (c *evalCtx) evalName(n Node, name string) resolve.ValueSet {
	if ph, ok := c.valueFieldPlaceholder(n, name); ok {
		return c.resolveConfig(ph)
	}
	if v, ok := c.symbolValue(name); ok {
		return v
	}
	return resolve.NewUnknown()
}

// evalFieldAccess handles X.Y: this.field routes to a @Value field / constant;
// otherwise the qualified name is looked up as a constant (Const.HOST).
func (c *evalCtx) evalFieldAccess(n Node) resolve.ValueSet {
	obj := n.ChildByFieldName("object")
	field := n.ChildByFieldName("field")
	if obj.Text() == "this" {
		if ph, ok := c.valueFieldPlaceholder(n, field.Text()); ok {
			return c.resolveConfig(ph)
		}
		if v, ok := c.symbolValue(field.Text()); ok {
			return v
		}
		return resolve.NewUnknown()
	}
	if v, ok := c.symbolValue(n.Text()); ok {
		return v
	}
	return resolve.NewUnknown()
}

// evalMethodInvocation handles the recognized builder: String.format. Everything
// else (getenv, getProperty, custom calls) is opaque.
func (c *evalCtx) evalMethodInvocation(n Node, depth int) resolve.ValueSet {
	if n.ChildByFieldName("object").Text() == "String" && n.ChildByFieldName("name").Text() == "format" {
		return c.evalStringFormat(n, depth)
	}
	return resolve.NewUnknown()
}

var formatSpecRe = regexp.MustCompile(`%[a-zA-Z]`)

// evalStringFormat reconstructs String.format("http://%s/u/%s", a, b) by
// interleaving the literal parts with the evaluated arguments.
func (c *evalCtx) evalStringFormat(n Node, depth int) resolve.ValueSet {
	args := namedChildrenOf(n.ChildByFieldName("arguments"))
	if len(args) == 0 || args[0].Type() != "string_literal" {
		return resolve.NewUnknown()
	}
	format := stripQuotes(args[0].Text())
	rest := args[1:]

	result := resolve.NewExact(model.Confirmed, "")
	last, argIdx := 0, 0
	for _, loc := range formatSpecRe.FindAllStringIndex(format, -1) {
		if loc[0] > last {
			result = resolve.Concat(result, resolve.NewExact(model.Confirmed, format[last:loc[0]]))
		}
		arg := resolve.NewUnknown()
		if argIdx < len(rest) {
			arg = c.eval(rest[argIdx], depth+1)
			argIdx++
		}
		result = resolve.Concat(result, arg)
		last = loc[1]
	}
	if last < len(format) {
		result = resolve.Concat(result, resolve.NewExact(model.Confirmed, format[last:]))
	}
	return result
}

// symbolValue resolves a constant reference (confirmed when found).
func (c *evalCtx) symbolValue(ref string) (resolve.ValueSet, bool) {
	if c.e.symbols == nil {
		return resolve.ValueSet{}, false
	}
	if v, ok := c.e.symbols.Constant(ref); ok {
		return resolve.NewExact(model.Confirmed, v), true
	}
	return resolve.ValueSet{}, false
}

// resolveConfig resolves a ${...} placeholder through the config/deploy layer.
func (c *evalCtx) resolveConfig(expr string) resolve.ValueSet {
	if c.e.config == nil {
		return resolve.NewUnknown()
	}
	if v, conf, ok := c.e.config.Resolve(expr); ok {
		return resolve.ExactValues(resolve.Value{S: v, Conf: conf})
	}
	return resolve.NewUnknown()
}

// valueFieldPlaceholder finds a field named `name` in the enclosing type and, if
// it carries @Value("${...}"), returns that placeholder.
func (c *evalCtx) valueFieldPlaceholder(n Node, name string) (string, bool) {
	cls := enclosingType(n)
	if !cls.Valid() {
		return "", false
	}
	body := cls.ChildByFieldName("body")
	for i := 0; i < body.NamedChildCount(); i++ {
		fd := body.NamedChild(i)
		if fd.Type() != "field_declaration" {
			continue
		}
		decl := directChild(fd, "variable_declarator")
		if !decl.Valid() || decl.ChildByFieldName("name").Text() != name {
			continue
		}
		if mods := directChild(fd, "modifiers"); mods.Valid() {
			return valueAnnotationArg(mods)
		}
		return "", false
	}
	return "", false
}

func enclosingType(n Node) Node {
	for p := n.Parent(); p.Valid(); p = p.Parent() {
		switch p.Type() {
		case "class_declaration", "enum_declaration", "interface_declaration":
			return p
		}
	}
	return Node{}
}

// valueAnnotationArg returns the string argument of a @Value annotation in a
// modifiers node. (A minimal local reader — the shared annotation helpers live
// in the Spring package, which java cannot import.)
func valueAnnotationArg(mods Node) (string, bool) {
	for i := 0; i < mods.NamedChildCount(); i++ {
		a := mods.NamedChild(i)
		if a.Type() != "annotation" || a.ChildByFieldName("name").Text() != "Value" {
			continue
		}
		args := a.ChildByFieldName("arguments")
		for j := 0; j < args.NamedChildCount(); j++ {
			if lit := args.NamedChild(j); lit.Type() == "string_literal" {
				return stripQuotes(lit.Text()), true
			}
		}
	}
	return "", false
}

func namedChildrenOf(n Node) []Node {
	out := make([]Node, 0, n.NamedChildCount())
	for i := 0; i < n.NamedChildCount(); i++ {
		out = append(out, n.NamedChild(i))
	}
	return out
}

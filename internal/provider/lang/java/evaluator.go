package java

import (
	"regexp"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/resolve"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// maxEvalDepth bounds expression recursion (defensive; deep chains and variable
// following are the real risk, guarded further in PR 15).
const maxEvalDepth = 20

// Evaluator implements provider.Resolver for Java: it walks a target expression
// (a URL/topic argument) and returns the possible string values as a ValueSet.
// It resolves literals, string concatenation, constants (SymbolIndex),
// @Value-injected fields (ConfigResolver), String.format, conditionals
// (ternary/switch -> union), and local variables (reaching definitions). Method
// params, loop-assigned variables, and opaque calls become holes.
type Evaluator struct {
	symbols provider.SymbolIndex
	config  provider.ConfigResolver
	types   schema.TypeSource
}

// NewEvaluator binds an evaluator to the built Index.
func NewEvaluator(idx *provider.Index) *Evaluator {
	e := &Evaluator{}
	if idx != nil {
		e.symbols = idx.Symbols
		e.config = idx.Config
		e.types = idx.Types
	}
	return e
}

// Resolve evaluates an AST expression node to a ValueSet.
func (e *Evaluator) Resolve(node provider.ASTNode) resolve.ValueSet {
	n, ok := node.(Node)
	if !ok || !n.Valid() {
		return resolve.NewUnknown()
	}
	c := &evalCtx{e: e, memo: map[memoKey]resolve.ValueSet{}, vars: map[string]bool{}}
	return c.eval(n, 0)
}

// evalCtx holds per-Resolve state. memo is keyed by node start byte (unique
// within one file) so a value revisited via variable-following is computed once;
// vars is the set of local variables currently being resolved (cycle guard for
// a = b; b = a).
type memoKey struct {
	file *File
	pos  uint32
}

type evalCtx struct {
	e    *Evaluator
	memo map[memoKey]resolve.ValueSet
	vars map[string]bool
}

func (c *evalCtx) eval(n Node, depth int) resolve.ValueSet {
	if !n.Valid() || depth > maxEvalDepth {
		return resolve.NewUnknown()
	}
	key := memoKey{file: n.file, pos: n.StartByte()}
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
	case "ternary_expression":
		return resolve.Union(
			c.eval(n.ChildByFieldName("consequence"), depth+1),
			c.eval(n.ChildByFieldName("alternative"), depth+1),
		)
	case "switch_expression":
		return c.evalSwitch(n, depth)
	case "identifier":
		return c.evalName(n, n.Text(), depth)
	case "field_access":
		return c.evalFieldAccess(n, depth)
	case "method_invocation":
		return c.evalMethodInvocation(n, depth)
	default:
		return resolve.NewUnknown()
	}
}

// evalSwitch unions the value of each arrow rule (case X -> expr) of a switch
// expression. Block-bodied rules and colon/yield switches are not analyzed.
func (c *evalCtx) evalSwitch(n Node, depth int) resolve.ValueSet {
	result := resolve.NewUnknown()
	n.Walk(func(m Node) bool {
		if m.Type() != "switch_rule" {
			return true
		}
		kids := namedChildrenOf(m)
		if len(kids) > 0 {
			v := kids[len(kids)-1] // last child = the rule's value
			// Arrow rules wrap the value in an expression_statement.
			if v.Type() == "expression_statement" && v.NamedChildCount() > 0 {
				v = v.NamedChild(0)
			}
			if v.Type() != "block" {
				result = resolve.Union(result, c.eval(v, depth+1))
			}
		}
		return false
	})
	return result
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

// evalName resolves a bare reference. A local variable takes precedence (Java
// scoping shadows fields); otherwise a @Value field, then an unambiguous
// constant. A method parameter (no local definition) stays unknown.
func (c *evalCtx) evalName(n Node, name string, depth int) resolve.ValueSet {
	if defs := c.reachingDefs(n, name); len(defs) > 0 {
		return c.evalDefs(name, defs, depth)
	}
	if ph, ok := c.valueFieldPlaceholder(n, name); ok {
		return c.resolveConfig(ph)
	}
	if v, ok := c.symbolValue(name); ok {
		return v
	}
	if v, ok := c.fieldInitializer(n, name, depth); ok {
		return v
	}
	if v, ok := c.ctorArgFromCreationSites(n, name, depth); ok {
		return v
	}
	if v, ok := c.paramCallSites(n, name, depth); ok {
		return v
	}
	return resolve.NewUnknown()
}

// ctorArgFromCreationSites follows a field ACROSS classes (IMPROVEMENTS #22):
// the field is set from a plain constructor parameter (`this.url = url`, no
// @Value), so its value comes from whoever writes `new ThisClass(...)`. We find
// those creation sites in the whole repo (Types index) and union the argument
// values, capped at likely. No sites / nothing resolvable -> stays a hole.
func (c *evalCtx) ctorArgFromCreationSites(n Node, field string, depth int) (resolve.ValueSet, bool) {
	types, ok := c.e.types.(*Types)
	if !ok {
		return resolve.ValueSet{}, false
	}
	cls := enclosingType(n)
	if !cls.Valid() {
		return resolve.ValueSet{}, false
	}
	clsName := cls.ChildByFieldName("name").Text()

	guard := "newarg:" + clsName + ":" + field
	if c.vars[guard] {
		return resolve.ValueSet{}, false
	}
	c.vars[guard] = true
	defer delete(c.vars, guard)

	argIdx := ctorParamIndexForField(cls, field)
	if argIdx < 0 {
		return resolve.ValueSet{}, false
	}
	result := resolve.NewUnknown()
	for _, args := range types.CreationSites(clsName) {
		if arg := args.NamedChild(argIdx); arg.Valid() {
			result = resolve.Union(result, c.eval(arg, depth+1))
		}
	}
	if result.Kind == resolve.Unknown {
		return resolve.ValueSet{}, false
	}
	return capLikely(result), true
}

// ctorParamIndexForField finds which constructor-parameter position feeds
// `this.<field> = <param>`, or -1.
func ctorParamIndexForField(cls Node, field string) int {
	body := cls.ChildByFieldName("body")
	for i := 0; i < body.NamedChildCount(); i++ {
		ctor := body.NamedChild(i)
		if ctor.Type() != "constructor_declaration" {
			continue
		}
		param := ""
		ctor.Walk(func(m Node) bool {
			if m.Type() == "assignment_expression" && m.ChildByFieldName("left").Text() == "this."+field {
				if r := m.ChildByFieldName("right"); r.Type() == "identifier" {
					param = r.Text()
				}
				return false
			}
			return true
		})
		if param == "" {
			continue
		}
		if idx := paramIndex(ctor, param); idx >= 0 {
			return idx
		}
	}
	return -1
}

// paramCallSites is the mirror of return-inlining (IMPROVEMENTS #13): when the
// name is a PARAMETER of the enclosing method, union the argument values from
// this method's call sites — first within the same class, then (IMPROVEMENTS
// #26) from receiver-qualified call sites across the whole repo where the
// receiver's declared type is this class (the thin-wrapper pattern:
// `eventProducer.send(TOPIC, msg)` feeding `kafkaTemplate.send(topic, ...)`).
// No resolvable call site anywhere -> stays a hole.
func (c *evalCtx) paramCallSites(n Node, name string, depth int) (resolve.ValueSet, bool) {
	m := enclosingMethod(n)
	if !m.Valid() || m.Type() != "method_declaration" {
		return resolve.ValueSet{}, false
	}
	idx := paramIndex(m, name)
	if idx < 0 {
		return resolve.ValueSet{}, false
	}
	methodName := m.ChildByFieldName("name").Text()
	guard := "arg:" + methodName + ":" + name
	if c.vars[guard] {
		return resolve.ValueSet{}, false
	}
	c.vars[guard] = true
	defer delete(c.vars, guard)

	cls := enclosingType(n)
	if !cls.Valid() {
		return resolve.ValueSet{}, false
	}
	result := resolve.NewUnknown()
	found := false
	cls.Walk(func(inv Node) bool {
		if inv.Type() != "method_invocation" || inv.ChildByFieldName("name").Text() != methodName {
			return true
		}
		if obj := inv.ChildByFieldName("object"); obj.Valid() && obj.Text() != "this" {
			return true // a different receiver — not this class's method
		}
		if arg := inv.ChildByFieldName("arguments").NamedChild(idx); arg.Valid() {
			result = resolve.Union(result, c.eval(arg, depth+1))
			found = true
		}
		return true
	})
	if found && result.Kind != resolve.Unknown {
		return result, true
	}
	return c.crossClassCallSites(cls, methodName, idx, depth)
}

// crossClassCallSites unions the argument values from receiver-qualified call
// sites of `<cls>.<methodName>` anywhere in the repo (IMPROVEMENTS #26). The
// receiver's DECLARED type must be the wrapper's class, so an unrelated `send`
// on some other type never contributes. Values are capped at likely — the flow
// crosses a class boundary.
func (c *evalCtx) crossClassCallSites(cls Node, methodName string, argIdx, depth int) (resolve.ValueSet, bool) {
	types, ok := c.e.types.(*Types)
	if !ok {
		return resolve.ValueSet{}, false
	}
	clsName := cls.ChildByFieldName("name").Text()
	if clsName == "" {
		return resolve.ValueSet{}, false
	}
	result := resolve.NewUnknown()
	found := false
	for _, inv := range types.CallSitesOf(methodName) {
		obj := inv.ChildByFieldName("object")
		recv := obj.Text()
		if obj.Type() == "field_access" && obj.ChildByFieldName("object").Text() == "this" {
			recv = obj.ChildByFieldName("field").Text()
		}
		if declaredTypeOf(inv, recv) != clsName {
			continue
		}
		if arg := inv.ChildByFieldName("arguments").NamedChild(argIdx); arg.Valid() {
			result = resolve.Union(result, c.eval(arg, depth+1))
			found = true
		}
	}
	if !found || result.Kind == resolve.Unknown {
		return resolve.ValueSet{}, false
	}
	return capLikely(result), true
}

// paramIndex is the position of `name` among a method's formal parameters, -1
// if it is not a parameter.
func paramIndex(method Node, name string) int {
	params := directChild(method, "formal_parameters")
	idx := 0
	for i := 0; i < params.NamedChildCount(); i++ {
		p := params.NamedChild(i)
		if p.Type() != "formal_parameter" {
			continue
		}
		if p.ChildByFieldName("name").Text() == name {
			return idx
		}
		idx++
	}
	return -1
}

// fieldInitializer resolves an instance field of the enclosing type through its
// initializer: `private String hostname = "http://visits-service/";`. The field
// is not final and could be reassigned at runtime, so the result is capped at
// likely (IMPROVEMENTS #3).
func (c *evalCtx) fieldInitializer(n Node, name string, depth int) (resolve.ValueSet, bool) {
	cls := enclosingType(n)
	if !cls.Valid() {
		return resolve.ValueSet{}, false
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
		if v := decl.ChildByFieldName("value"); v.Valid() {
			return capLikely(c.eval(v, depth+1)), true
		}
		return resolve.ValueSet{}, false // field exists but has no initializer
	}
	return resolve.ValueSet{}, false
}

// capLikely lowers every confirmed value to likely (mutable-source cap).
func capLikely(vs resolve.ValueSet) resolve.ValueSet {
	if vs.Kind != resolve.Exact {
		return vs
	}
	vals := make([]resolve.Value, len(vs.Values))
	for i, v := range vs.Values {
		if v.Conf == model.Confirmed {
			v.Conf = model.Likely
		}
		vals[i] = v
	}
	return resolve.ExactValues(vals...)
}

// reachingDefs returns the value expressions assigned to a local variable that
// (a) are in the same method, (b) textually precede the use — a safe
// over-approximation of reaching definitions — and (c) are NOT inside a loop
// (loop iterations aren't analyzed). An empty result means param / no local def.
func (c *evalCtx) reachingDefs(use Node, name string) []Node {
	method := enclosingMethod(use)
	if !method.Valid() {
		return nil
	}
	before := use.StartByte()
	var defs []Node
	method.Walk(func(m Node) bool {
		if isLoop(m.Type()) {
			return false // don't descend into loop bodies
		}
		switch m.Type() {
		case "local_variable_declaration":
			for _, d := range namedChildrenOf(m) {
				if d.Type() == "variable_declarator" && d.ChildByFieldName("name").Text() == name {
					if v := d.ChildByFieldName("value"); v.Valid() && v.StartByte() < before {
						defs = append(defs, v)
					}
				}
			}
		case "assignment_expression":
			if m.ChildByFieldName("left").Text() == name {
				if v := m.ChildByFieldName("right"); v.Valid() && v.StartByte() < before {
					defs = append(defs, v)
				}
			}
		}
		return true
	})
	return defs
}

// evalDefs unions the values of a variable's definitions, guarding against a
// self-referential cycle (a = b; b = a).
func (c *evalCtx) evalDefs(name string, defs []Node, depth int) resolve.ValueSet {
	if c.vars[name] {
		return resolve.NewUnknown() // cycle
	}
	c.vars[name] = true
	defer delete(c.vars, name)

	result := resolve.NewUnknown()
	for _, d := range defs {
		result = resolve.Union(result, c.eval(d, depth+1))
	}
	return result
}

func enclosingMethod(n Node) Node {
	for p := n.Parent(); p.Valid(); p = p.Parent() {
		switch p.Type() {
		case "method_declaration", "constructor_declaration", "lambda_expression":
			return p
		}
	}
	return Node{}
}

func isLoop(typ string) bool {
	switch typ {
	case "for_statement", "enhanced_for_statement", "while_statement", "do_statement":
		return true
	}
	return false
}

// evalFieldAccess handles X.Y: this.field routes to a @Value field / constant /
// field initializer; otherwise the qualified name is looked up as a constant.
func (c *evalCtx) evalFieldAccess(n Node, depth int) resolve.ValueSet {
	obj := n.ChildByFieldName("object")
	field := n.ChildByFieldName("field")
	if obj.Text() == "this" {
		if ph, ok := c.valueFieldPlaceholder(n, field.Text()); ok {
			return c.resolveConfig(ph)
		}
		if v, ok := c.symbolValue(field.Text()); ok {
			return v
		}
		if v, ok := c.fieldInitializer(n, field.Text(), depth); ok {
			return v
		}
		return resolve.NewUnknown()
	}
	if v, ok := c.symbolValue(n.Text()); ok {
		return v
	}
	return resolve.NewUnknown()
}

// evalMethodInvocation handles: String.format; a DiscoveryClient chain
// (getInstances("name")... -> http://name, IMPROVEMENTS #4); and a same-class
// helper method with a single return statement, which is inlined one level
// (return-inlining). Everything else (getenv, getProperty, external calls)
// stays opaque.
func (c *evalCtx) evalMethodInvocation(n Node, depth int) resolve.ValueSet {
	obj := n.ChildByFieldName("object")
	name := n.ChildByFieldName("name").Text()

	if obj.Text() == "String" && name == "format" {
		return c.evalStringFormat(n, depth)
	}
	// System.getenv("KEY") / getProperty: the KEY is a literal, and the
	// deploy/config layer may know it (IMPROVEMENTS #12).
	if obj.Text() == "System" && (name == "getenv" || name == "getProperty") {
		args := n.ChildByFieldName("arguments")
		if a := args.NamedChild(0); a.Valid() && a.Type() == "string_literal" {
			return c.resolveConfig("${" + stripQuotes(a.Text()) + "}")
		}
		return resolve.NewUnknown()
	}
	// props.getPaymentUrl() on a @ConfigurationProperties bean — THE standard
	// enterprise config style (IMPROVEMENTS #11).
	if v, ok := c.configBeanGetter(n, obj, name); ok {
		return v
	}
	// discoveryClient.getInstances("customers-service").get(0).getUri():
	// scan the receiver chain for getInstances with a literal service name.
	// The static answer is the logical service target (as an lb-style URL),
	// capped at likely — which instance answers is a runtime matter.
	for cur := n; cur.Valid() && cur.Type() == "method_invocation"; cur = cur.ChildByFieldName("object") {
		if cur.ChildByFieldName("name").Text() == "getInstances" {
			args := cur.ChildByFieldName("arguments")
			if arg := args.NamedChild(0); arg.Valid() && arg.Type() == "string_literal" {
				return resolve.ExactValues(resolve.Value{S: "http://" + stripQuotes(arg.Text()), Conf: model.Likely})
			}
		}
	}
	// Same-class helper with no receiver: inline its single return expression.
	if !obj.Valid() {
		if v, ok := c.inlineLocalMethod(n, name, depth); ok {
			return v
		}
	}
	return resolve.NewUnknown()
}

// configBeanGetter resolves `props.getServiceUrl()` where `props` is declared
// (field or method param) with a type carrying @ConfigurationProperties: the
// getter maps to `<prefix>.<property>` and resolves through the config layer.
func (c *evalCtx) configBeanGetter(call, obj Node, method string) (resolve.ValueSet, bool) {
	if c.e.types == nil || !obj.Valid() {
		return resolve.ValueSet{}, false
	}
	prop := getterProperty(method)
	if prop == "" {
		return resolve.ValueSet{}, false
	}
	recv := obj.Text()
	if obj.Type() == "field_access" && obj.ChildByFieldName("object").Text() == "this" {
		recv = obj.ChildByFieldName("field").Text()
	}
	typeName := declaredTypeOf(call, recv)
	if typeName == "" {
		return resolve.ValueSet{}, false
	}
	td, ok := c.e.types.Lookup(typeName)
	if !ok || !td.HasAnnotation("ConfigurationProperties") {
		return resolve.ValueSet{}, false
	}
	prefix := ""
	for _, a := range td.Annotations {
		if a.Name == "ConfigurationProperties" {
			prefix = a.Arg
			if prefix == "" {
				prefix = a.Named["prefix"]
			}
			if prefix == "" {
				prefix = a.Named["value"]
			}
		}
	}
	// Relaxed binding: the yml may spell the key kebab-case or camelCase.
	for _, key := range []string{joinProp(prefix, kebab(prop)), joinProp(prefix, prop)} {
		if v := c.resolveConfig("${" + key + "}"); v.Kind != resolve.Unknown {
			return v, true
		}
	}
	return resolve.ValueSet{}, false
}

// declaredTypeOf finds the declared type (generics stripped) of a name in the
// enclosing method's parameters or the enclosing class's fields.
func declaredTypeOf(n Node, name string) string {
	if m := enclosingMethod(n); m.Valid() {
		if params := directChild(m, "formal_parameters"); params.Valid() {
			for i := 0; i < params.NamedChildCount(); i++ {
				p := params.NamedChild(i)
				if p.Type() == "formal_parameter" && p.ChildByFieldName("name").Text() == name {
					return simpleTypeName(p.ChildByFieldName("type").Text())
				}
			}
		}
	}
	cls := enclosingType(n)
	if !cls.Valid() {
		return ""
	}
	body := cls.ChildByFieldName("body")
	for i := 0; i < body.NamedChildCount(); i++ {
		fd := body.NamedChild(i)
		if fd.Type() != "field_declaration" {
			continue
		}
		decl := directChild(fd, "variable_declarator")
		if decl.Valid() && decl.ChildByFieldName("name").Text() == name {
			return simpleTypeName(fd.ChildByFieldName("type").Text())
		}
	}
	return ""
}

// kebab converts camelCase to kebab-case: serviceUrl -> service-url.
func kebab(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func joinProp(prefix, prop string) string {
	if prefix == "" {
		return prop
	}
	return prefix + "." + prop
}

// inlineLocalMethod finds a method of the enclosing type with the given name
// and exactly one return statement, and evaluates that returned expression.
// Guarded against recursion by the method-name set; parameters naturally
// become holes.
func (c *evalCtx) inlineLocalMethod(call Node, name string, depth int) (resolve.ValueSet, bool) {
	if c.vars["method:"+name] {
		return resolve.ValueSet{}, false // recursive helper — give up
	}
	cls := enclosingType(call)
	if !cls.Valid() {
		return resolve.ValueSet{}, false
	}
	body := cls.ChildByFieldName("body")
	for i := 0; i < body.NamedChildCount(); i++ {
		m := body.NamedChild(i)
		if m.Type() != "method_declaration" || m.ChildByFieldName("name").Text() != name {
			continue
		}
		ret, count := singleReturnExpr(m)
		if count != 1 || !ret.Valid() {
			return resolve.ValueSet{}, false // zero or several returns — too risky
		}
		c.vars["method:"+name] = true
		defer delete(c.vars, "method:"+name)
		return c.eval(ret, depth+1), true
	}
	return resolve.ValueSet{}, false
}

// singleReturnExpr returns the expression of the method's return statement and
// how many return statements the method has.
func singleReturnExpr(method Node) (Node, int) {
	var expr Node
	count := 0
	method.Walk(func(n Node) bool {
		if n.Type() == "return_statement" {
			count++
			if n.NamedChildCount() > 0 {
				expr = n.NamedChild(0)
			}
		}
		return true
	})
	return expr, count
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
	if v, conf, src, ok := c.e.config.Resolve(expr); ok {
		return resolve.ExactValues(resolve.Value{S: v, Conf: conf, Source: src})
	}
	return resolve.NewUnknown()
}

// valueFieldPlaceholder finds the @Value("...") string behind a field named
// `name` in the enclosing type: on the field declaration itself, or — the
// idiomatic constructor-injection style (IMPROVEMENTS #8) — on a constructor
// parameter that is assigned to the field (`this.name = param`).
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
			if ph, ok := valueAnnotationArg(mods); ok {
				return ph, true
			}
		}
		break // field exists but carries no @Value — try the constructors
	}
	return ctorParamValue(body, name)
}

// ctorParamValue scans the type's constructors for `this.<field> = <param>`
// where <param> carries @Value("..."), and returns that annotation string.
func ctorParamValue(body Node, field string) (string, bool) {
	for i := 0; i < body.NamedChildCount(); i++ {
		ctor := body.NamedChild(i)
		if ctor.Type() != "constructor_declaration" {
			continue
		}
		// Which parameter feeds the field? Default: one with the same name;
		// otherwise follow the `this.field = param` assignment.
		param := field
		ctor.Walk(func(m Node) bool {
			if m.Type() == "assignment_expression" && m.ChildByFieldName("left").Text() == "this."+field {
				if r := m.ChildByFieldName("right"); r.Type() == "identifier" {
					param = r.Text()
				}
				return false
			}
			return true
		})
		params := directChild(ctor, "formal_parameters")
		for j := 0; j < params.NamedChildCount(); j++ {
			p := params.NamedChild(j)
			if p.Type() != "formal_parameter" || p.ChildByFieldName("name").Text() != param {
				continue
			}
			if mods := directChild(p, "modifiers"); mods.Valid() {
				if ph, ok := valueAnnotationArg(mods); ok {
					return ph, true
				}
			}
		}
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

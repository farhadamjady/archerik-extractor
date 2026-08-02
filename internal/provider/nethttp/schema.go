package nethttp

import (
	"sort"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/golang"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// typeIndexer builds Index.Types from the repo's Go struct types so the route
// detector can resolve request/response body schemas.
type typeIndexer struct{}

func (typeIndexer) Name() string { return "nethttp.types" }

func (typeIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	idx.Types = buildTypeIndex(goFiles(ic.Parsed))
	return nil
}

// goFiles returns the parsed Go files in stable path order.
func goFiles(parsed map[string]provider.ParsedFile) []*golang.File {
	var paths []string
	for p, pf := range parsed {
		if _, ok := pf.(*golang.File); ok {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	files := make([]*golang.File, 0, len(paths))
	for _, p := range paths {
		files = append(files, parsed[p].(*golang.File))
	}
	return files
}

// handlerSchemas best-effort resolves a net/http handler's request and response
// body schemas. Go handlers carry no declared types, so this walks the handler's
// body for JSON decode/encode calls (stdlib `json.Unmarshal`/`Encode` or a
// project helper whose name contains Decode/Unmarshal / Encode/Marshal/JSON) and
// infers the body type from the locally-declared variable passed to it. The
// handler is a func literal or a same-file named function/method value.
func handlerSchemas(file *golang.File, handler golang.Node, walker *schema.Walker) (req, resp *model.Schema) {
	body := handlerBody(file, handler)
	if !body.Valid() {
		return nil, nil
	}
	locals := localTypes(body)
	if rt := decodeArgType(body, locals); rt != "" {
		req = bodyOrNil(walker.Type(normalizeGoType(rt)))
	}
	if rt := encodeArgType(body, locals); rt != "" {
		resp = bodyOrNil(walker.Type(normalizeGoType(rt)))
	}
	return req, resp
}

// handlerBody resolves the block body of a handler argument: a func literal
// inline, or a named function/method (getUsers / h.GetUsers) declared in the same
// file, matched by name.
func handlerBody(file *golang.File, handler golang.Node) golang.Node {
	if handler.Type() == "func_literal" {
		return handler.ChildByFieldName("body")
	}
	name := handlerName(handler)
	if name == "" {
		return golang.Node{}
	}
	var body golang.Node
	file.Root().Walk(func(n golang.Node) bool {
		if body.Valid() {
			return false
		}
		if n.Type() == "function_declaration" || n.Type() == "method_declaration" {
			if nm := n.ChildByFieldName("name"); nm.Valid() && nm.Text() == name {
				body = n.ChildByFieldName("body")
				return false
			}
		}
		return true
	})
	return body
}

// handlerName extracts the referenced function name from a handler expression:
// `getUsers` (identifier) or `h.GetUsers` (selector -> field).
func handlerName(handler golang.Node) string {
	switch handler.Type() {
	case "identifier":
		return handler.Text()
	case "selector_expression":
		if f := handler.ChildByFieldName("field"); f.Valid() {
			return f.Text()
		}
	}
	return ""
}

// localTypes maps a handler's locally-declared variable names to their declared
// or composite-literal type text: `var x T` and `x := T{...}` / `x := []T{...}`.
func localTypes(body golang.Node) map[string]string {
	locals := map[string]string{}
	body.Walk(func(n golang.Node) bool {
		switch n.Type() {
		case "var_spec":
			ty := n.ChildByFieldName("type")
			if ty.Valid() {
				for _, nm := range identNames(n) {
					locals[nm] = ty.Text()
				}
			}
		case "short_var_declaration":
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			ls := golang.NamedChildren(left)
			rs := golang.NamedChildren(right)
			if len(ls) == 1 && len(rs) == 1 && ls[0].Type() == "identifier" {
				if t := compositeType(rs[0]); t != "" {
					locals[ls[0].Text()] = t
				}
			}
		}
		return true
	})
	return locals
}

// identNames returns the identifier(s) a var_spec declares.
func identNames(spec golang.Node) []string {
	var out []string
	for _, c := range golang.NamedChildren(spec) {
		if c.Type() == "identifier" {
			out = append(out, c.Text())
		}
	}
	return out
}

// compositeType returns the type text of a composite literal (`T{...}` /
// `[]T{...}` / `pkg.T{...}`), or "" when the expression is not one.
func compositeType(expr golang.Node) string {
	if expr.Type() != "composite_literal" {
		return ""
	}
	if ty := expr.ChildByFieldName("type"); ty.Valid() {
		return ty.Text()
	}
	return ""
}

// decodeArgType finds the request body type: the first call whose function name
// signals JSON decoding (Unmarshal/Decode/Bind) and whose `&var` argument has a
// known local type.
func decodeArgType(body golang.Node, locals map[string]string) string {
	found := ""
	body.Walk(func(n golang.Node) bool {
		if found != "" || n.Type() != "call_expression" {
			return found == ""
		}
		if !isDecodeCall(callName(n)) {
			return true
		}
		for _, a := range callArgs(n) {
			if v := addrOfIdent(a); v != "" {
				if t, ok := locals[v]; ok {
					found = t
					return false
				}
			}
		}
		return true
	})
	return found
}

// encodeArgType finds the response body type: across calls that signal JSON
// encoding (Marshal/Encode/JSON/Respond), the last argument that resolves to a
// known non-error struct — last call wins (the happy-path response, after the
// early error returns).
func encodeArgType(body golang.Node, locals map[string]string) string {
	found := ""
	body.Walk(func(n golang.Node) bool {
		if n.Type() != "call_expression" {
			return true
		}
		name := callName(n)
		if !isEncodeCall(name) || isDecodeCall(name) {
			return true
		}
		for _, a := range callArgs(n) {
			t := argType(a, locals)
			if t == "" || strings.Contains(t, "Error") {
				continue // skip error-response envelopes
			}
			found = t // last qualifying call+arg wins
		}
		return true
	})
	return found
}

// argType resolves an encode argument to a type: a composite literal's type, or
// a local variable's declared type. Returns "" for non-type args (w, status).
func argType(arg golang.Node, locals map[string]string) string {
	if t := compositeType(arg); t != "" {
		return t
	}
	if arg.Type() == "identifier" {
		return locals[arg.Text()]
	}
	return ""
}

// addrOfIdent returns the identifier of a `&x` unary expression, or "".
func addrOfIdent(n golang.Node) string {
	if n.Type() != "unary_expression" {
		return ""
	}
	if op := n.ChildByFieldName("operand"); op.Valid() && op.Type() == "identifier" {
		return op.Text()
	}
	return ""
}

// callName returns a call's function name: `f` (identifier) or the trailing
// `.Method` of a selector chain (json.NewEncoder(w).Encode -> "Encode").
func callName(call golang.Node) string {
	fn := call.ChildByFieldName("function")
	switch fn.Type() {
	case "identifier":
		return fn.Text()
	case "selector_expression":
		if f := fn.ChildByFieldName("field"); f.Valid() {
			return f.Text()
		}
	}
	return ""
}

// callArgs returns a call's argument expressions.
func callArgs(call golang.Node) []golang.Node {
	if a := call.ChildByFieldName("arguments"); a.Valid() {
		return golang.NamedChildren(a)
	}
	return nil
}

func isDecodeCall(name string) bool {
	return strings.Contains(name, "Unmarshal") || strings.Contains(name, "Decode") || strings.Contains(name, "Bind")
}

func isEncodeCall(name string) bool {
	return strings.Contains(name, "Marshal") || strings.Contains(name, "Encode") ||
		strings.Contains(name, "JSON") || strings.Contains(name, "Respond")
}

// bodyOrNil drops a void/nil schema (no request/response body).
func bodyOrNil(s *model.Schema) *model.Schema {
	if s == nil || s.Type == "void" {
		return nil
	}
	return s
}

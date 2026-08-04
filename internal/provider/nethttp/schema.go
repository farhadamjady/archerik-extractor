package nethttp

import (
	"sort"
	"strconv"
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
	files := goFiles(ic.Parsed)
	idx.Types = buildTypeIndex(files)
	idx.GoFuncBodies = buildFuncIndex(files)
	return nil
}

// buildFuncIndex maps every function/method's simple name to its body node,
// across all files, so a handler referenced by name resolves regardless of which
// file declares it. Files arrive path-sorted (goFiles), and the first
// declaration of a name wins, so a method-name collision across receivers
// resolves deterministically (best-effort — the selector carries no receiver
// type).
func buildFuncIndex(files []*golang.File) map[string]provider.ASTNode {
	idx := map[string]provider.ASTNode{}
	for _, f := range files {
		f.Root().Walk(func(n golang.Node) bool {
			switch n.Type() {
			case "function_declaration", "method_declaration":
				name := n.ChildByFieldName("name")
				body := n.ChildByFieldName("body")
				if name.Valid() && body.Valid() {
					if _, exists := idx[name.Text()]; !exists {
						idx[name.Text()] = body
					}
				}
			}
			return true
		})
	}
	return idx
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
// handler is a func literal, a same-file named function/method, or (via funcs) a
// named function/method declared in another file.
func handlerSchemas(file *golang.File, handler golang.Node, walker *schema.Walker, funcs map[string]provider.ASTNode) (req, resp *model.Schema) {
	body := handlerBody(file, handler, funcs)
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
// inline, or a named function/method (getUsers / h.GetUsers) resolved by name —
// first in the same file, then through the repo-wide func index (funcs), so a
// handler defined in another file still resolves.
func handlerBody(file *golang.File, handler golang.Node, funcs map[string]provider.ASTNode) golang.Node {
	if handler.Type() == "func_literal" {
		return handler.ChildByFieldName("body")
	}
	name := handlerName(handler)
	if name == "" {
		return golang.Node{}
	}
	if body := sameFileFuncBody(file, name); body.Valid() {
		return body
	}
	if n, ok := funcs[name]; ok {
		if body, ok := n.(golang.Node); ok {
			return body
		}
	}
	return golang.Node{}
}

// sameFileFuncBody finds a named function/method's body within one file (the
// fast path, and the fallback when no repo-wide index is present).
func sameFileFuncBody(file *golang.File, name string) golang.Node {
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

// encodeArgType finds the response body type by scanning the handler body block
// by block. Within a block, statements run in order, so a `WriteHeader(status)`
// call sets the branch's HTTP status for the Encode calls that follow it, and an
// Encode call may itself carry the status (`ReturnJSON(w, status, x)`). Encodes
// on an ERROR branch (4xx/5xx) are error envelopes — a `map[string]string{"error":
// …}` 404 shape is NOT the endpoint's contract — so they are skipped rather than
// presented as the response (finding #58). Among the remaining success/neutral
// Encodes the last resolvable argument wins (the happy path, after early returns).
func encodeArgType(body golang.Node, locals map[string]string) string {
	found := ""
	var scan func(block golang.Node)
	scan = func(block golang.Node) {
		branchErr := false // an error-status WriteHeader seen earlier in THIS block
		for _, stmt := range golang.NamedChildren(block) {
			if call := topCall(stmt); call.Valid() {
				switch name := callName(call); {
				case name == "WriteHeader":
					if cls := statusClassOfArgs(callArgs(call)); cls != statusUnknown {
						branchErr = cls == statusError
					}
				case isEncodeCall(name) && !isDecodeCall(name):
					if branchErr || statusClassOfArgs(callArgs(call)) == statusError {
						break // error envelope — never the contract
					}
					for _, a := range callArgs(call) {
						t := argType(a, locals)
						if t == "" || strings.Contains(t, "Error") {
							continue
						}
						found = t // last qualifying success Encode wins
					}
				}
			}
			for _, b := range outerBlocks(stmt) {
				scan(b) // if/else/for/switch bodies carry their own branch status
			}
		}
	}
	scan(body)
	return found
}

// topCall returns the top-level call expression of a statement (an
// `expression_statement` like `w.WriteHeader(...)` / `json.NewEncoder(w).Encode(x)`,
// or a `return f(...)`), or an invalid node when the statement is not a bare call.
func topCall(stmt golang.Node) golang.Node {
	switch stmt.Type() {
	case "expression_statement", "return_statement":
		for _, c := range golang.NamedChildren(stmt) {
			if c.Type() == "call_expression" {
				return c
			}
		}
	}
	return golang.Node{}
}

// outerBlocks returns the outermost `block` nodes structurally contained in a
// statement (an if/else consequence+alternative, a for/switch body), WITHOUT
// descending into blocks-within-blocks — scan() recurses into those itself. This
// keeps each block's WriteHeader→Encode branch status local to that block.
func outerBlocks(stmt golang.Node) []golang.Node {
	var out []golang.Node
	var rec func(x golang.Node)
	rec = func(x golang.Node) {
		for _, c := range golang.NamedChildren(x) {
			if c.Type() == "block" {
				out = append(out, c)
			} else {
				rec(c) // descend through non-block children (condition, else-if, …)
			}
		}
	}
	rec(stmt)
	return out
}

type statusClass int

const (
	statusUnknown statusClass = iota
	statusSuccess
	statusError
)

// statusClassOfArgs classifies the first HTTP-status argument in a call's args:
// a `http.StatusXxx` selector/identifier or a numeric literal. 2xx → success,
// 4xx/5xx → error, anything else/none → unknown.
func statusClassOfArgs(args []golang.Node) statusClass {
	for _, a := range args {
		if c := classifyStatusExpr(a); c != statusUnknown {
			return c
		}
	}
	return statusUnknown
}

func classifyStatusExpr(n golang.Node) statusClass {
	switch n.Type() {
	case "selector_expression":
		if f := n.ChildByFieldName("field"); f.Valid() {
			return classifyStatusName(f.Text())
		}
	case "identifier":
		return classifyStatusName(n.Text())
	case "int_literal":
		if v, err := strconv.Atoi(n.Text()); err == nil {
			switch {
			case v >= 200 && v < 300:
				return statusSuccess
			case v >= 400 && v < 600:
				return statusError
			}
		}
	}
	return statusUnknown
}

// statusSuccessNames / statusErrorNames are the net/http StatusXxx constants we
// classify; an unlisted name stays statusUnknown (never taints a branch).
var statusSuccessNames = map[string]bool{
	"StatusOK": true, "StatusCreated": true, "StatusAccepted": true,
	"StatusNonAuthoritativeInfo": true, "StatusNoContent": true,
	"StatusResetContent": true, "StatusPartialContent": true,
	"StatusMultiStatus": true, "StatusAlreadyReported": true, "StatusIMUsed": true,
}

var statusErrorNames = map[string]bool{
	"StatusBadRequest": true, "StatusUnauthorized": true, "StatusPaymentRequired": true,
	"StatusForbidden": true, "StatusNotFound": true, "StatusMethodNotAllowed": true,
	"StatusNotAcceptable": true, "StatusProxyAuthRequired": true, "StatusRequestTimeout": true,
	"StatusConflict": true, "StatusGone": true, "StatusPreconditionFailed": true,
	"StatusRequestEntityTooLarge": true, "StatusUnsupportedMediaType": true,
	"StatusUnprocessableEntity": true, "StatusTooManyRequests": true,
	"StatusInternalServerError": true, "StatusNotImplemented": true, "StatusBadGateway": true,
	"StatusServiceUnavailable": true, "StatusGatewayTimeout": true,
}

func classifyStatusName(name string) statusClass {
	switch {
	case statusSuccessNames[name]:
		return statusSuccess
	case statusErrorNames[name]:
		return statusError
	}
	return statusUnknown
}

// argType resolves an encode argument to a type: an address-of expression
// (`&x` / `&T{...}`, e.g. `json.Marshal(&resp)`) unwrapped to its operand, a
// composite literal's type, or a local variable's declared type. Returns "" for
// non-type args (w, status).
func argType(arg golang.Node, locals map[string]string) string {
	if arg.Type() == "unary_expression" {
		if op := arg.ChildByFieldName("operator"); op.Valid() && op.Text() != "&" {
			return "" // only address-of carries a body type (&x); skip !x, -x, <-ch
		}
		return argType(arg.ChildByFieldName("operand"), locals)
	}
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

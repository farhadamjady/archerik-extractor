package springkt

import (
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/kotlin"
	"github.com/farhadamjady/archerik-extractor/internal/schema"
)

// restDetector extracts REST endpoints from @RestController classes written in
// Kotlin. Mirrors the Java Spring REST detector's semantics (class
// @RequestMapping + method mapping = composed path, verb kept, path variables
// preserved) but over the tree-sitter-kotlin AST: class_declaration >
// class_body > function_declaration, with annotations under `modifiers`.
type restDetector struct{}

func (restDetector) Name() string             { return "springkt.rest" }
func (restDetector) Protocol() model.Protocol { return model.ProtoREST }

// controllerQuery captures any class with at least one annotation; the handler
// decides whether it is a controller.
const controllerQuery = `(class_declaration (modifiers (annotation)) ) @class`

func (d restDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: controllerQuery, OnMatch: d.onController}}
}

var mappingVerb = map[string]string{
	"GetMapping":    "GET",
	"PostMapping":   "POST",
	"PutMapping":    "PUT",
	"DeleteMapping": "DELETE",
	"PatchMapping":  "PATCH",
}

func (restDetector) onController(mc *provider.MatchContext) {
	class, ok := mc.Captures["class"].(kotlin.Node)
	if !ok || !class.Valid() {
		return
	}
	mods := kotlin.Modifiers(class)
	isCtrl, needResponseBody := controllerKind(mods)
	if !isCtrl {
		return
	}
	bases := classBasePaths(mods)
	walker := schema.NewWalkerDepth(mc.Index.Types, mc.Index.SchemaDepth)
	// #66: resolve an expression-body handler's response from the delegate call.
	ctrlFields := controllerFields(class)
	body := kotlin.ChildByType(class, "class_body")
	if !body.Valid() {
		return
	}
	for _, fn := range kotlin.NamedChildren(body) {
		if fn.Type() != "function_declaration" {
			continue
		}
		fmods := kotlin.Modifiers(fn)
		if needResponseBody && !kotlin.FindAnnotation(fmods, "ResponseBody").Valid() {
			continue
		}
		req, resp := funcSchemas(walker, fn, ctrlFields, mc.Index.MethodReturns)
		appendMethodEndpoints(mc.Out, fmods, bases, req, resp)
	}
}

// funcSchemas resolves a handler function's request (the @RequestBody parameter)
// and response (the return type) schemas through the shared walker. A void/Unit
// body yields nil; a nullable return/param (`T?`) marks the schema nullable.
func funcSchemas(w *schema.Walker, fn kotlin.Node, ctrlFields map[string]string, methodReturns map[string]map[string]string) (req, resp *model.Schema) {
	if rt, nullable := kotlin.ReturnType(fn); rt != "" {
		resp = bodyOrNil(w.Type(rt), nullable)
	} else {
		// No declared return type — infer from an expression body (#66).
		resp = inferResponseFromExprBody(fn, ctrlFields, methodReturns, w)
	}
	for _, p := range kotlin.Params(fn) {
		if p.Mods.Valid() && kotlin.FindAnnotation(p.Mods, "RequestBody").Valid() {
			typ, nullable := kotlin.DeclaredType(p.Node)
			req = bodyOrNil(w.Type(typ), nullable)
			break
		}
	}
	return req, resp
}

// bodyOrNil drops a void schema (no request/response body) and applies top-level
// nullability from a Kotlin `T?` type.
func bodyOrNil(s *model.Schema, nullable bool) *model.Schema {
	if s == nil || s.Type == "void" {
		return nil
	}
	if nullable {
		s.Nullable = true
	}
	return s
}

// controllerKind classifies the class: @RestController (every mapped method is
// REST), @Controller + class-level @ResponseBody (same), or plain @Controller
// (only @ResponseBody methods are REST — the classic style).
func controllerKind(mods kotlin.Node) (isController, needResponseBody bool) {
	if !mods.Valid() {
		return false, false
	}
	plain := false
	for _, a := range kotlin.AnnotationsOf(mods) {
		switch kotlin.AnnotationName(a) {
		case "RestController":
			return true, false
		case "Controller":
			plain = true
		}
	}
	if !plain {
		return false, false
	}
	if kotlin.FindAnnotation(mods, "ResponseBody").Valid() {
		return true, false
	}
	return true, true
}

// classBasePaths is the class-level @RequestMapping path(s), or [""] when absent.
func classBasePaths(mods kotlin.Node) []string {
	if ann := kotlin.FindAnnotation(mods, "RequestMapping"); ann.Valid() {
		if paths, _, ok := kotlin.AnnotationStringValues(ann, "value", "path"); ok && len(paths) > 0 {
			return paths
		}
	}
	return []string{""}
}

// appendMethodEndpoints emits the endpoints for one handler function: its first
// mapping annotation, across the product of class base paths × method paths × verbs.
// req/resp are the resolved request/response body schemas (nil when absent).
func appendMethodEndpoints(out *model.Service, fmods kotlin.Node, bases []string, req, resp *model.Schema) {
	if !fmods.Valid() {
		return
	}
	for _, ann := range kotlin.AnnotationsOf(fmods) {
		name := kotlin.AnnotationName(ann)
		var verbs []string
		switch {
		case mappingVerb[name] != "":
			verbs = []string{mappingVerb[name]}
		case name == "RequestMapping":
			verbs = requestMappingVerbs(ann)
		default:
			continue
		}

		subs, literal, ok := kotlin.AnnotationStringValues(ann, "value", "path")
		if !ok || len(subs) == 0 {
			subs = []string{""}
		}
		conf := model.Confirmed
		if !literal {
			conf = model.Uncertain
		}
		for _, base := range bases {
			for _, sub := range subs {
				path := joinPath(base, sub)
				for _, v := range verbs {
					out.Endpoints = append(out.Endpoints, model.Endpoint{
						Method:     v,
						Path:       path,
						Request:    req,
						Response:   resp,
						Protocol:   model.ProtoREST,
						Detection:  model.DetectAnnotation,
						Confidence: conf,
					})
				}
			}
		}
		return // one mapping annotation per handler
	}
}

// requestMappingVerbs reads method = [RequestMethod.X] from a @RequestMapping, or
// "*" (any verb) when absent.
func requestMappingVerbs(ann kotlin.Node) []string {
	var out []string
	ann.Walk(func(n kotlin.Node) bool {
		if n.Type() == "simple_identifier" {
			switch t := n.Text(); t {
			case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS", "TRACE":
				out = append(out, t)
			}
		}
		return true
	})
	if len(out) == 0 {
		return []string{"*"}
	}
	return out
}

// joinPath composes a class base path with a method sub-path.
func joinPath(base, sub string) string {
	b := strings.Trim(base, "/")
	s := strings.Trim(sub, "/")
	switch {
	case b == "" && s == "":
		return "/"
	case b == "":
		return "/" + s
	case s == "":
		return "/" + b
	default:
		return "/" + b + "/" + s
	}
}

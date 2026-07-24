package micronaut

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// restDetector extracts REST endpoints from Micronaut @Controller classes. In
// Micronaut @Controller is the REST controller (unlike Spring, where @Controller
// is MVC and only @ResponseBody methods are REST) — every mapped method is an
// endpoint. The query captures any annotated class; the handler decides if it is
// a @Controller and walks it so path composition (class base + method mapping)
// happens in Go. Verbs are kept and path variables (/pets/{id}) preserved; a
// method path is never emitted alone.
type restDetector struct{}

func (restDetector) Name() string             { return "micronaut.rest" }
func (restDetector) Protocol() model.Protocol { return model.ProtoREST }

// controllerQuery captures ANY annotated class; the handler filters to
// @Controller. A class with several annotations fires once per annotation; the
// identical endpoints collapse in the marshal dedup.
const controllerQuery = `(class_declaration
  (modifiers [
    (marker_annotation name: (identifier) @_ann)
    (annotation name: (identifier) @_ann)
  ])
) @class`

func (d restDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: controllerQuery, OnMatch: d.onController}}
}

// mappingVerb maps Micronaut's method mapping annotations to their HTTP verb.
var mappingVerb = map[string]string{
	"Get":     "GET",
	"Post":    "POST",
	"Put":     "PUT",
	"Delete":  "DELETE",
	"Patch":   "PATCH",
	"Head":    "HEAD",
	"Options": "OPTIONS",
	"Trace":   "TRACE",
}

func (restDetector) onController(mc *provider.MatchContext) {
	class, ok := mc.Captures["class"].(java.Node)
	if !ok || !class.Valid() {
		return
	}
	mods := java.ChildByType(class, "modifiers")
	if !mods.Valid() || !java.FindAnnotation(mods, "Controller").Valid() {
		return
	}
	bases := controllerBasePaths(mods)
	walker := schema.NewWalker(mc.Index.Types)
	body := class.ChildByFieldName("body")
	for _, m := range java.NamedChildren(body) {
		if m.Type() != "method_declaration" {
			continue
		}
		req, resp := methodSchemas(walker, m)
		appendMethodEndpoints(mc.Out, m, bases, req, resp)
	}

	// API-interface pattern: the @Controller may `implements` (or `extends`) a
	// type whose methods carry the mappings — usually in a sibling *-api module
	// the detector never scans. The contract indexer captured those method nodes
	// (from Parsed + Shared); compose each with the controller base path. Identical
	// endpoints (a method annotated on both class and interface) collapse in dedup.
	for _, iface := range implementedTypes(class) {
		for _, node := range mc.Index.HTTPContracts[iface] {
			m, ok := node.(java.Node)
			if !ok || !m.Valid() {
				continue
			}
			req, resp := methodSchemas(walker, m)
			appendMethodEndpoints(mc.Out, m, bases, req, resp)
		}
	}
}

// implementedTypes returns the simple names of the interfaces a class implements
// plus its superclass — the types whose declarative HTTP mappings a @Controller
// inherits. tree-sitter models these as super_interfaces (an interface list) and
// superclass children of the class_declaration.
func implementedTypes(class java.Node) []string {
	var out []string
	if si := java.ChildByType(class, "super_interfaces"); si.Valid() {
		si.Walk(func(n java.Node) bool {
			if n.Type() == "type_identifier" {
				out = append(out, n.Text())
			}
			return true
		})
	}
	if sc := java.ChildByType(class, "superclass"); sc.Valid() {
		sc.Walk(func(n java.Node) bool {
			if n.Type() == "type_identifier" {
				out = append(out, n.Text())
			}
			return true
		})
	}
	return out
}

// controllerBasePaths is the path prefix(es) from @Controller — the positional
// string, value=, or {"/a","/b"} — or [""] when absent (Micronaut defaults "/").
func controllerBasePaths(mods java.Node) []string {
	ann := java.FindAnnotation(mods, "Controller")
	if paths, _, ok := java.AnnotationStringValues(ann, "value"); ok && len(paths) > 0 {
		return paths
	}
	return []string{""}
}

// methodSchemas resolves a handler method's request (the @Body parameter) and
// response (the return type) schemas. A void body yields nil.
func methodSchemas(w *schema.Walker, method java.Node) (req, resp *model.Schema) {
	if ret := method.ChildByFieldName("type"); ret.Valid() {
		resp = bodyOrNil(w.Type(ret.Text()))
	}
	if params := java.ChildByType(method, "formal_parameters"); params.Valid() {
		for _, p := range java.NamedChildren(params) {
			if p.Type() != "formal_parameter" {
				continue
			}
			if mods := java.ChildByType(p, "modifiers"); mods.Valid() && java.FindAnnotation(mods, "Body").Valid() {
				req = bodyOrNil(w.Type(p.ChildByFieldName("type").Text()))
				break
			}
		}
	}
	return req, resp
}

func bodyOrNil(s *model.Schema) *model.Schema {
	if s == nil || s.Type == "void" {
		return nil
	}
	return s
}

// appendMethodEndpoints emits the endpoints for one handler method: its first
// mapping annotation, expanded across the cartesian product of controller base
// paths, method paths (value=/uri=/uris=/positional), and the verb.
func appendMethodEndpoints(out *model.Service, method java.Node, bases []string, req, resp *model.Schema) {
	mods := java.ChildByType(method, "modifiers")
	if !mods.Valid() {
		return
	}
	for _, ann := range java.AnnotationsOf(mods) {
		name := java.AnnotationName(ann)
		verb := mappingVerb[name]
		if verb == "" {
			continue
		}
		subs, literal, ok := java.AnnotationStringValues(ann, "value", "uri", "uris")
		if !ok || len(subs) == 0 {
			subs = []string{""} // no path attribute — maps to the controller base
		}
		conf := model.Confirmed
		if !literal {
			conf = model.Uncertain // computed/placeholder path — resolver comes later
		}
		for _, base := range bases {
			for _, sub := range subs {
				path := joinPath(base, sub)
				out.Endpoints = append(out.Endpoints, model.Endpoint{
					Method:     verb,
					Path:       path,
					Request:    req,
					Response:   resp,
					Protocol:   model.ProtoREST,
					Detection:  model.DetectAnnotation,
					Confidence: conf,
				})
			}
		}
		return // one mapping annotation per handler method
	}
}

// joinPath composes a controller base path with a method sub-path into a single
// leading-slash path, preserving path variables and avoiding double slashes.
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

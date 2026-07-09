package spring

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// restDetector extracts REST endpoints from @RestController classes. The query
// matches a controller class and captures the whole class node; the handler
// walks it so path composition (class @RequestMapping + method mapping) happens
// in Go, where it is far clearer than in a query. Verb is kept and path
// variables (/users/{id}) are preserved; a method path is never emitted alone.
type restDetector struct{}

func (restDetector) Name() string             { return "spring.rest" }
func (restDetector) Protocol() model.Protocol { return model.ProtoREST }

// restControllerQuery captures any class annotated @RestController. @_ann is
// bound by whichever branch matches (marker vs argumented) and filtered to
// RestController; @class is the class node the handler walks.
const restControllerQuery = `(class_declaration
  (modifiers [
    (marker_annotation name: (identifier) @_ann)
    (annotation name: (identifier) @_ann)
  ])
  (#eq? @_ann "RestController")
) @class`

func (d restDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: restControllerQuery, OnMatch: d.onController}}
}

// mappingVerb maps the shortcut mapping annotations to their HTTP verb.
var mappingVerb = map[string]string{
	"GetMapping":    "GET",
	"PostMapping":   "POST",
	"PutMapping":    "PUT",
	"DeleteMapping": "DELETE",
	"PatchMapping":  "PATCH",
}

func (restDetector) onController(mc *provider.MatchContext) {
	class, ok := mc.Captures["class"].(java.Node)
	if !ok || !class.Valid() {
		return
	}
	bases := classBasePaths(class)
	walker := schema.NewWalker(mc.Index.Types)
	body := class.ChildByFieldName("body")
	for _, m := range namedChildren(body) {
		if m.Type() == "method_declaration" {
			req, resp := methodSchemas(walker, m)
			appendMethodEndpoints(mc.Out, m, bases, req, resp)
		}
	}
}

// methodSchemas resolves a handler method's request (the @RequestBody parameter,
// ignoring @Valid) and response (the return type) schemas. A void body yields nil.
func methodSchemas(w *schema.Walker, method java.Node) (req, resp *model.Schema) {
	if ret := method.ChildByFieldName("type"); ret.Valid() {
		resp = bodyOrNil(w.Type(ret.Text()))
	}
	if params := childByType(method, "formal_parameters"); params.Valid() {
		for _, p := range namedChildren(params) {
			if p.Type() != "formal_parameter" {
				continue
			}
			if mods := childByType(p, "modifiers"); mods.Valid() && findAnnotation(mods, "RequestBody").Valid() {
				req = bodyOrNil(w.Type(p.ChildByFieldName("type").Text()))
				break
			}
		}
	}
	return req, resp
}

// bodyOrNil drops a void schema (no request/response body).
func bodyOrNil(s *model.Schema) *model.Schema {
	if s == nil || s.Type == "void" {
		return nil
	}
	return s
}

// classBasePaths is the path prefix(es) from a class-level @RequestMapping, or
// [""] when absent. Usually one, but a class may declare a base-path array.
func classBasePaths(class java.Node) []string {
	mods := childByType(class, "modifiers")
	if !mods.Valid() {
		return []string{""}
	}
	ann := findAnnotation(mods, "RequestMapping")
	if !ann.Valid() {
		return []string{""}
	}
	paths, _, ok := annotationStringValues(ann, "value", "path")
	if !ok || len(paths) == 0 {
		return []string{""}
	}
	return paths
}

// appendMethodEndpoints emits the endpoints for a single handler method: its
// first mapping annotation, expanded across the cartesian product of class base
// paths, method paths, and verbs — so @GetMapping({"/a","/b"}) and method-array
// @RequestMapping each yield every combination Spring would register.
func appendMethodEndpoints(out *model.Service, method java.Node, bases []string, req, resp *model.Schema) {
	mods := childByType(method, "modifiers")
	if !mods.Valid() {
		return
	}
	for _, ann := range annotationsOf(mods) {
		name := annotationName(ann)
		var verbs []string
		switch {
		case mappingVerb[name] != "":
			verbs = []string{mappingVerb[name]}
		case name == "RequestMapping":
			verbs = requestMappingVerbs(ann)
		default:
			continue
		}

		subs, literal, ok := annotationStringValues(ann, "value", "path")
		if !ok || len(subs) == 0 {
			subs = []string{""} // no path on the method — maps to the base path
		}
		conf := model.Confirmed
		if !literal {
			conf = model.Uncertain // computed/placeholder path — resolver comes later
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
		return // one mapping annotation per handler method
	}
}

// requestMappingVerbs reads method=RequestMethod.X (or an array of them) from a
// @RequestMapping. With no method attribute it maps to any verb, emitted as "*".
func requestMappingVerbs(ann java.Node) []string {
	args := ann.ChildByFieldName("arguments")
	if args.Valid() {
		for _, c := range namedChildren(args) {
			if c.Type() == "element_value_pair" && c.ChildByFieldName("key").Text() == "method" {
				return verbNames(c.ChildByFieldName("value"))
			}
		}
	}
	return []string{"*"}
}

func verbNames(v java.Node) []string {
	// Annotation array values (method={...}) are element_value_array_initializer.
	if v.Type() == "array_initializer" || v.Type() == "element_value_array_initializer" {
		var out []string
		for _, e := range namedChildren(v) {
			if n := verbName(e); n != "" {
				out = append(out, n)
			}
		}
		if len(out) > 0 {
			return out
		}
		return []string{"*"}
	}
	if n := verbName(v); n != "" {
		return []string{n}
	}
	return []string{"*"}
}

// verbName pulls "GET" out of RequestMethod.GET (or a bare GET).
func verbName(v java.Node) string {
	if f := v.ChildByFieldName("field"); f.Valid() {
		return f.Text()
	}
	t := v.Text()
	if i := strings.LastIndex(t, "."); i >= 0 {
		return t[i+1:]
	}
	return t
}

// joinPath composes a class base path with a method sub-path into a single
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

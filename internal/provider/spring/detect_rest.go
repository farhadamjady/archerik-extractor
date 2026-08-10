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

// restControllerQuery captures ANY annotated class; the handler decides if it
// is a controller — directly @RestController, or via a META-annotation whose
// declaration carries @RestController. A class with several annotations fires
// the handler once per annotation; the identical endpoints collapse in the
// marshal dedup.
const restControllerQuery = `(class_declaration
  (modifiers [
    (marker_annotation name: (identifier) @_ann)
    (annotation name: (identifier) @_ann)
  ])
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
	types := mc.Index.Types
	mods := childByType(class, "modifiers")
	isCtrl, needResponseBody := controllerKind(mods, types)
	if !isCtrl {
		return
	}
	bases := classBasePaths(class, types)
	walker := schema.NewWalkerDepth(types, mc.Index.SchemaDepth)
	body := class.ChildByFieldName("body")
	for _, m := range namedChildren(body) {
		if m.Type() != "method_declaration" {
			continue
		}
		// Classic @Controller style: only methods marked @ResponseBody are REST
		// endpoints; the rest render views.
		if needResponseBody && !methodHasResponseBody(m) {
			continue
		}
		req, resp := methodSchemas(walker, m)
		appendMethodEndpoints(mc.Out, m, bases, req, resp, types)
	}
}

// controllerKind classifies the class (directly or via a meta-annotation):
//   - @RestController            -> controller, every mapped method is REST
//   - @Controller + @ResponseBody at class level -> same
//   - @Controller                -> controller, but ONLY @ResponseBody methods
//     are REST (the classic pre-@RestController enterprise style, #19)
func controllerKind(mods java.Node, types schema.TypeSource) (isController, needResponseBody bool) {
	if !mods.Valid() {
		return false, false
	}
	plainController := false
	for _, a := range annotationsOf(mods) {
		name := annotationName(a)
		switch name {
		case "RestController":
			return true, false
		case "Controller":
			plainController = true
		case "ResponseBody":
			if plainController {
				return true, false
			}
		default:
			if md, ok := metaDef(types, name); ok {
				if md.HasAnnotation("RestController") {
					return true, false
				}
				if md.HasAnnotation("Controller") {
					plainController = true
				}
			}
		}
	}
	if plainController {
		// class-level @ResponseBody may appear before @Controller in the loop
		for _, a := range annotationsOf(mods) {
			if annotationName(a) == "ResponseBody" {
				return true, false
			}
		}
		return true, true
	}
	return false, false
}

func methodHasResponseBody(m java.Node) bool {
	mods := childByType(m, "modifiers")
	return mods.Valid() && findAnnotation(mods, "ResponseBody").Valid()
}

// metaDef resolves an annotation name to its @interface declaration, if that
// declaration is in the repo.
func metaDef(types schema.TypeSource, name string) (*schema.TypeDef, bool) {
	if types == nil {
		return nil, false
	}
	td, ok := types.Lookup(name)
	if !ok || td.Kind != schema.KindAnnotation {
		return nil, false
	}
	return td, true
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

// classBasePaths is the path prefix(es) from a class-level @RequestMapping —
// direct, or carried by a meta-annotation's declaration — or [""] when absent.
func classBasePaths(class java.Node, types schema.TypeSource) []string {
	mods := childByType(class, "modifiers")
	if !mods.Valid() {
		return []string{""}
	}
	if ann := findAnnotation(mods, "RequestMapping"); ann.Valid() {
		if paths, _, ok := annotationStringValues(ann, "value", "path"); ok && len(paths) > 0 {
			return paths
		}
		return []string{""}
	}
	// meta: @MyController = @RestController + @RequestMapping("/api")
	for _, a := range annotationsOf(mods) {
		if md, ok := metaDef(types, annotationName(a)); ok {
			for _, meta := range md.Annotations {
				if meta.Name == "RequestMapping" && meta.Arg != "" {
					return []string{meta.Arg}
				}
			}
		}
	}
	return []string{""}
}

// appendMethodEndpoints emits the endpoints for a single handler method: its
// first mapping annotation, expanded across the cartesian product of class base
// paths, method paths, and verbs — so @GetMapping({"/a","/b"}) and method-array
// @RequestMapping each yield every combination Spring would register.
func appendMethodEndpoints(out *model.Service, method java.Node, bases []string, req, resp *model.Schema, types schema.TypeSource) {
	mods := childByType(method, "modifiers")
	if !mods.Valid() {
		return
	}
	for _, ann := range annotationsOf(mods) {
		name := annotationName(ann)
		var verbs []string
		var metaPath string
		switch {
		case mappingVerb[name] != "":
			verbs = []string{mappingVerb[name]}
		case name == "RequestMapping":
			verbs = requestMappingVerbs(ann)
		default:
			// meta-annotation: @MyGet declared with @GetMapping("/path")
			md, ok := metaDef(types, name)
			if !ok {
				continue
			}
			for _, meta := range md.Annotations {
				if v := mappingVerb[meta.Name]; v != "" {
					verbs, metaPath = []string{v}, meta.Arg
					break
				}
				if meta.Name == "RequestMapping" {
					verbs, metaPath = []string{"*"}, meta.Arg
					break
				}
			}
			if len(verbs) == 0 {
				continue
			}
		}

		subs, literal, ok := annotationStringValues(ann, "value", "path")
		if !ok || len(subs) == 0 {
			subs = []string{metaPath} // usage path wins; else the meta's path; else base
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

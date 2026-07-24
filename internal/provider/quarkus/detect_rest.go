package quarkus

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// restDetector extracts JAX-RS REST endpoints. A resource is a class annotated
// with @Path; each method carrying an HTTP-verb annotation (@GET/@POST/...) is an
// endpoint. Distinct from Spring/Micronaut: the verb and the path are SEPARATE
// annotations — the verb is @GET (no argument), the path is a separate @Path on
// the method (absent = the class base path). Path composition, verbs, and path
// variables (/pets/{id}) work as elsewhere; a method path is never emitted alone.
type restDetector struct{}

func (restDetector) Name() string             { return "quarkus.rest" }
func (restDetector) Protocol() model.Protocol { return model.ProtoREST }

// resourceQuery captures any annotated class; the handler filters to @Path.
const resourceQuery = `(class_declaration
  (modifiers [
    (marker_annotation name: (identifier) @_ann)
    (annotation name: (identifier) @_ann)
  ])
) @class`

func (d restDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: resourceQuery, OnMatch: d.onResource}}
}

// jaxrsVerbs is the set of JAX-RS HTTP-method annotations; the annotation's
// simple name IS the HTTP verb.
var jaxrsVerbs = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "DELETE": true,
	"PATCH": true, "HEAD": true, "OPTIONS": true,
}

// jaxrsParamAnnotations mark a method parameter as NON-body (path/query/header/
// context/etc.). A parameter with none of these is the request-body entity.
var jaxrsParamAnnotations = map[string]bool{
	"PathParam": true, "QueryParam": true, "HeaderParam": true,
	"CookieParam": true, "FormParam": true, "MatrixParam": true,
	"BeanParam": true, "Context": true, "RestPath": true, "RestQuery": true,
	"RestHeader": true, "RestCookie": true, "RestForm": true, "RestMatrix": true,
}

func (restDetector) onResource(mc *provider.MatchContext) {
	class, ok := mc.Captures["class"].(java.Node)
	if !ok || !class.Valid() {
		return
	}
	mods := java.ChildByType(class, "modifiers")
	if !mods.Valid() || !java.FindAnnotation(mods, "Path").Valid() {
		return
	}
	base := resourceBasePath(mods)
	walker := schema.NewWalker(mc.Index.Types)
	body := class.ChildByFieldName("body")
	for _, m := range java.NamedChildren(body) {
		if m.Type() != "method_declaration" {
			continue
		}
		appendMethodEndpoints(mc.Out, m, base, walker)
	}

	// API-interface pattern (#41): the @Path resource may implement an interface
	// whose methods carry the verb+@Path annotations (very common in JAX-RS,
	// including OpenAPI-generated server interfaces present in source).
	for _, iface := range implementedTypes(class) {
		for _, node := range mc.Index.HTTPContracts[iface] {
			if m, ok := node.(java.Node); ok && m.Valid() {
				appendMethodEndpoints(mc.Out, m, base, walker)
			}
		}
	}
}

// resourceBasePath is the class-level @Path value, or "" when absent.
func resourceBasePath(mods java.Node) string {
	ann := java.FindAnnotation(mods, "Path")
	if vals, _, ok := java.AnnotationStringValues(ann, "value"); ok && len(vals) > 0 {
		return vals[0]
	}
	return ""
}

// appendMethodEndpoints emits the endpoint for a method that carries a verb
// annotation: verb from @GET/@POST/..., path from the method @Path (else the
// class base), request body from the non-JAX-RS-annotated parameter, response
// from the return type (unwrapping reactive Uni<T>/Multi<T> wrappers).
func appendMethodEndpoints(out *model.Service, method java.Node, base string, walker *schema.Walker) {
	mods := java.ChildByType(method, "modifiers")
	if !mods.Valid() {
		return
	}
	verb := ""
	for _, a := range java.AnnotationsOf(mods) {
		if name := java.AnnotationName(a); jaxrsVerbs[name] {
			verb = name
			break
		}
	}
	if verb == "" {
		return // not an endpoint (a helper, or a sub-resource locator)
	}

	subs := []string{""}
	conf := model.Confirmed
	if pann := java.FindAnnotation(mods, "Path"); pann.Valid() {
		if vals, literal, ok := java.AnnotationStringValues(pann, "value"); ok && len(vals) > 0 {
			subs = vals
			if !literal {
				conf = model.Uncertain
			}
		}
	}

	req, resp := methodSchemas(walker, method)
	for _, sub := range subs {
		out.Endpoints = append(out.Endpoints, model.Endpoint{
			Method:     verb,
			Path:       joinPath(base, sub),
			Request:    req,
			Response:   resp,
			Protocol:   model.ProtoREST,
			Detection:  model.DetectAnnotation,
			Confidence: conf,
		})
	}
}

// methodSchemas resolves the request (body entity parameter) and response
// (return type) schemas. Reactive wrappers Uni<T>/Multi<T> unwrap to T.
func methodSchemas(w *schema.Walker, method java.Node) (req, resp *model.Schema) {
	if ret := method.ChildByFieldName("type"); ret.Valid() {
		resp = bodyOrNil(w.Type(unwrapReactive(ret.Text())))
	}
	if params := java.ChildByType(method, "formal_parameters"); params.Valid() {
		for _, p := range java.NamedChildren(params) {
			if p.Type() != "formal_parameter" {
				continue
			}
			if isBodyParam(p) {
				req = bodyOrNil(w.Type(p.ChildByFieldName("type").Text()))
				break
			}
		}
	}
	return req, resp
}

// isBodyParam reports whether a parameter is the request-body entity: it carries
// none of the JAX-RS parameter annotations (@PathParam/@QueryParam/@Context/...).
func isBodyParam(p java.Node) bool {
	mods := java.ChildByType(p, "modifiers")
	if !mods.Valid() {
		return true // no annotations at all — a plain entity parameter
	}
	for _, a := range java.AnnotationsOf(mods) {
		if jaxrsParamAnnotations[java.AnnotationName(a)] {
			return false
		}
	}
	return true
}

// unwrapReactive strips a Mutiny/reactive container so the schema is the payload
// type: Uni<Fight> -> Fight, Multi<Hero> -> Hero, RestResponse<X> -> X.
func unwrapReactive(t string) string {
	for _, w := range []string{"Uni", "Multi", "CompletionStage", "CompletableFuture", "RestResponse"} {
		if strings.HasPrefix(t, w+"<") && strings.HasSuffix(t, ">") {
			return strings.TrimSpace(t[len(w)+1 : len(t)-1])
		}
	}
	return t
}

func bodyOrNil(s *model.Schema) *model.Schema {
	if s == nil || s.Type == "void" {
		return nil
	}
	return s
}

// implementedTypes returns the interfaces a class implements plus its superclass.
func implementedTypes(class java.Node) []string {
	var out []string
	for _, kind := range []string{"super_interfaces", "superclass"} {
		if n := java.ChildByType(class, kind); n.Valid() {
			n.Walk(func(x java.Node) bool {
				if x.Type() == "type_identifier" {
					out = append(out, x.Text())
				}
				return true
			})
		}
	}
	return out
}

// joinPath composes a base path with a sub-path into a single leading-slash path.
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

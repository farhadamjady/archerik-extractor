package nestjs

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// restDetector extracts REST endpoints from NestJS @Controller classes. A
// controller class carries @Controller (optional base path); each method with an
// HTTP-verb decorator (@Get/@Post/...) is an endpoint whose path is the method
// decorator's argument composed onto the base. NestJS path params use the `:id`
// syntax; they are normalized to `{id}` so the graph is uniform across languages.
type restDetector struct{}

func (restDetector) Name() string             { return "nestjs.rest" }
func (restDetector) Protocol() model.Protocol { return model.ProtoREST }

// controllerQuery captures every class; the handler filters to @Controller
// (decorators are preceding siblings, so they aren't part of this pattern).
const controllerQuery = `(class_declaration) @class`

func (d restDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: controllerQuery, OnMatch: d.onController}}
}

var verbDecorator = map[string]string{
	"Get": "GET", "Post": "POST", "Put": "PUT", "Delete": "DELETE",
	"Patch": "PATCH", "Options": "OPTIONS", "Head": "HEAD", "All": "*",
}

func (restDetector) onController(mc *provider.MatchContext) {
	class, ok := mc.Captures["class"].(tsjs.Node)
	if !ok || !class.Valid() {
		return
	}
	classDecs := tsjs.ClassDecorators(class)
	ctrl := tsjs.FindDecorator(classDecs, "Controller")
	if !ctrl.Valid() {
		return
	}
	base := controllerBase(ctrl)
	// setGlobalPrefix('api') applies to every controller (the "*" slot of
	// MountPrefixes, filled by the global-prefix indexer).
	globals := []string{""}
	if gp := mc.Index.MountPrefixes["*"]; len(gp) > 0 {
		globals = gp
	}

	body := tsjs.ChildByType(class, "class_body")
	if !body.Valid() {
		return
	}
	walker := schema.NewWalkerDepth(mc.Index.Types, mc.Index.SchemaDepth)
	aliases := typeAliases(mc.Index.Types)
	for _, m := range tsjs.NamedChildren(body) {
		if m.Type() != "method_definition" {
			continue
		}
		appendMethodEndpoints(mc.Out, m, base, globals, walker, aliases)
	}
}

// typeAliases returns the local type-alias map when the type source is the
// TS index (it always is for this provider); nil otherwise.
func typeAliases(ts schema.TypeSource) map[string]tsAlias {
	if ti, ok := ts.(*tsTypeIndex); ok {
		return ti.aliases
	}
	return nil
}

// controllerBase extracts the controller base path from @Controller: the
// positional string (@Controller('cats')) or the object form
// (@Controller({ path: 'cats', version: '1' })). A non-literal/absent path is "".
func controllerBase(ctrl tsjs.Node) string {
	if v, literal, ok := tsjs.DecoratorStringArg(ctrl); ok && literal {
		return v
	}
	if v, literal, ok := tsjs.ObjectStringProp(ctrl, "path"); ok && literal {
		return v
	}
	return ""
}

// appendMethodEndpoints emits the endpoint for a method that carries an HTTP-verb
// decorator, composing global prefix + controller base + method path, and
// attaching request/response body schemas resolved from the method signature.
func appendMethodEndpoints(out *model.Service, method tsjs.Node, base string, globals []string, walker *schema.Walker, aliases map[string]tsAlias) {
	decs := tsjs.PrecedingDecorators(method)
	for _, dec := range decs {
		verb, isVerb := verbDecorator[tsjs.DecoratorName(dec)]
		if !isVerb {
			continue
		}
		sub, literal, ok := tsjs.DecoratorStringArg(dec)
		conf := model.Confirmed
		if !ok {
			sub = "" // @Get() with no path -> the controller base
		} else if !literal {
			conf = model.Uncertain // computed path
		}
		req := methodRequest(method, walker, aliases)
		resp := methodResponse(method, walker, aliases)
		for _, g := range globals {
			out.Endpoints = append(out.Endpoints, model.Endpoint{
				Method:     verb,
				Path:       normalizePath(joinPath(strings.Trim(g, "/")+"/"+strings.Trim(base, "/"), sub)),
				Request:    req,
				Response:   resp,
				Protocol:   model.ProtoREST,
				Detection:  model.DetectAnnotation,
				Confidence: conf,
			})
		}
		return // one verb decorator per method
	}
}

// methodResponse resolves the response body schema from a handler's declared
// return type (`: Promise<ArticleRO>` -> ArticleRO), dropping void/absent. Local
// nullable aliases and `| null` unions resolve to the base type with nullable set
// (#61): `Promise<NullableType<User>>` / `Promise<User | null>` -> User, nullable.
func methodResponse(method tsjs.Node, walker *schema.Walker, aliases map[string]tsAlias) *model.Schema {
	rt := method.ChildByFieldName("return_type")
	if !rt.Valid() {
		return nil
	}
	nt, nullable := normalizeTypeAlias(typeText(rt), aliases)
	s := bodyOrNil(walker.Type(nt))
	if s != nil && nullable {
		s.Nullable = true
	}
	return s
}

// methodRequest resolves the request body schema from the parameter decorated
// with @Body (`@Body() dto: CreateArticleDto` -> CreateArticleDto). A
// @Body('field') that picks a sub-property still yields the declared param type,
// the closest static contract available.
func methodRequest(method tsjs.Node, walker *schema.Walker, aliases map[string]tsAlias) *model.Schema {
	params := method.ChildByFieldName("parameters")
	if !params.Valid() {
		return nil
	}
	for _, p := range tsjs.NamedChildren(params) {
		for _, d := range tsjs.NamedChildren(p) {
			if d.Type() == "decorator" && tsjs.DecoratorName(d) == "Body" {
				if ta := p.ChildByFieldName("type"); ta.Valid() {
					nt, nullable := normalizeTypeAlias(typeText(ta), aliases)
					s := bodyOrNil(walker.Type(nt))
					if s != nil && nullable {
						s.Nullable = true
					}
					// @Body('key') binds a SUB-property: the wire body is
					// { key: <Dto> }, so wrap the DTO under that key (#63).
					if key, literal, ok := tsjs.DecoratorStringArg(d); ok && literal && key != "" {
						s = wrapUnderKey(s, key)
					}
					return s
				}
			}
		}
	}
	return nil
}

// wrapUnderKey wraps a resolved body schema as { <key>: <schema> } — the shape a
// @Body('key') param actually receives on the wire. A nil body stays nil.
func wrapUnderKey(s *model.Schema, key string) *model.Schema {
	if s == nil {
		return nil
	}
	inner := *s
	inner.Name = key
	return &model.Schema{
		Type:       "object",
		Confidence: s.Confidence,
		Nested:     []model.Schema{inner},
	}
}

// bodyOrNil drops a void schema (no request/response body).
func bodyOrNil(s *model.Schema) *model.Schema {
	if s == nil || s.Type == "void" {
		return nil
	}
	return s
}

// joinPath composes a controller base path with a method sub-path.
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

// normalizePath converts Express/NestJS `:param` path segments to the `{param}`
// form used by the rest of the graph, so a Spring caller's `/cats/{id}` matches a
// NestJS `/cats/:id` endpoint. A wildcard `*` segment is left as-is.
func normalizePath(p string) string {
	if !strings.Contains(p, ":") {
		return p
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, ":") {
			name := strings.TrimSuffix(strings.TrimPrefix(s, ":"), "?")
			segs[i] = "{" + name + "}"
		}
	}
	return strings.Join(segs, "/")
}

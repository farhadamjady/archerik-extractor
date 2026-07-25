package nestjs

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
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

	body := tsjs.ChildByType(class, "class_body")
	if !body.Valid() {
		return
	}
	for _, m := range tsjs.NamedChildren(body) {
		if m.Type() != "method_definition" {
			continue
		}
		appendMethodEndpoints(mc.Out, m, base)
	}
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
// decorator, composing its path with the controller base path.
func appendMethodEndpoints(out *model.Service, method tsjs.Node, base string) {
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
		out.Endpoints = append(out.Endpoints, model.Endpoint{
			Method:     verb,
			Path:       normalizePath(joinPath(base, sub)),
			Protocol:   model.ProtoREST,
			Detection:  model.DetectAnnotation,
			Confidence: conf,
		})
		return // one verb decorator per method
	}
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

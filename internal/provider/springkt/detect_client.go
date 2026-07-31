package springkt

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/kotlin"
)

// clientDetector extracts outbound HTTP dependencies from Kotlin Spring services:
//
//   - @FeignClient(name = "payment-service", url = "...")  — declarative client
//   - restTemplate.getForObject/exchange/postForObject/...("http://...", ...)
//
// Feign emits the raw logical name as target_name (backend maps name ->
// service_id; CLAUDE.md rule 4), confirmed. A RestTemplate call with a literal
// absolute URL resolves to its authority; a relative path or dynamic URL emits
// an honest uncertain edge. WebClient builder chains (.get().uri(...)) and
// config-placeholder resolution are later rounds.
type clientDetector struct{}

func (clientDetector) Name() string             { return "springkt.client" }
func (clientDetector) Protocol() model.Protocol { return model.ProtoREST }

const (
	feignClassQuery = `(class_declaration (modifiers (annotation))) @class`

	restCallQuery = `(call_expression
  (navigation_expression
    (navigation_suffix (simple_identifier) @method))
  (call_suffix (value_arguments) @args)) @call`
)

func (d clientDetector) Rules() []provider.Rule {
	return []provider.Rule{
		{Query: feignClassQuery, OnMatch: d.onFeign},
		{Query: restCallQuery, OnMatch: d.onRestCall},
	}
}

// onFeign emits a Feign edge for an interface/class annotated @FeignClient.
func (clientDetector) onFeign(mc *provider.MatchContext) {
	class, ok := mc.Captures["class"].(kotlin.Node)
	if !ok || !class.Valid() {
		return
	}
	ann := kotlin.FindAnnotation(kotlin.Modifiers(class), "FeignClient")
	if !ann.Valid() {
		return
	}
	dep := model.Dependency{Protocol: model.ProtoREST, Detection: model.DetectFeign, Confidence: model.Confirmed}
	if names, literal, ok := kotlin.AnnotationStringValues(ann, "name", "value"); ok && len(names) > 0 && literal {
		dep.TargetName, dep.Resolved = names[0], true
	}
	if urls, literal, ok := kotlin.AnnotationStringValues(ann, "url"); ok && len(urls) > 0 && literal {
		dep.URL = urls[0]
		if dep.TargetName == "" {
			if host := authority(urls[0]); host != "" {
				dep.TargetName, dep.Resolved = host, true
			}
		}
	}
	if dep.TargetName == "" {
		// @FeignClient with a computed name/url — still an edge, but unnamed.
		dep.Confidence = model.Uncertain
	}
	mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, dep)
}

// restTemplateMethod is the set of RestTemplate methods whose first argument is
// the request URL.
var restTemplateMethod = map[string]bool{
	"getForObject": true, "getForEntity": true,
	"postForObject": true, "postForEntity": true, "postForLocation": true,
	"put": true, "delete": true, "patchForObject": true, "exchange": true,
}

// onRestCall emits an edge for a RestTemplate call carrying a literal URL.
func (clientDetector) onRestCall(mc *provider.MatchContext) {
	method, _ := mc.Captures["method"].(kotlin.Node)
	args, _ := mc.Captures["args"].(kotlin.Node)
	if !method.Valid() || !restTemplateMethod[method.Text()] {
		return
	}
	first := firstArg(args)
	if !first.Valid() || first.Type() != "string_literal" {
		return // dynamic/absent URL: RestTemplate calls are common enough that a
		// non-literal here is not worth an anonymous edge without type tracking
	}
	url := stringLiteralValue(first)
	dep := model.Dependency{Protocol: model.ProtoREST, Detection: model.DetectRestTemplate}
	if host := authority(url); host != "" {
		dep.TargetName, dep.URL, dep.Resolved, dep.Confidence = host, url, true, model.Confirmed
	} else {
		dep.URL, dep.Confidence = url, model.Uncertain
	}
	mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, dep)
}

// firstArg returns the first value_argument's expression node.
func firstArg(args kotlin.Node) kotlin.Node {
	for _, a := range kotlin.NamedChildren(args) {
		if a.Type() != "value_argument" {
			continue
		}
		if kids := kotlin.NamedChildren(a); len(kids) > 0 {
			return kids[0]
		}
	}
	return kotlin.Node{}
}

// stringLiteralValue returns a Kotlin string_literal's content without the
// surrounding quotes, concatenating its literal content segments. An
// interpolated ${...} segment contributes nothing (making the value a partial),
// which keeps a templated URL from resolving to a bogus host.
func stringLiteralValue(n kotlin.Node) string {
	var b strings.Builder
	for _, c := range kotlin.NamedChildren(n) {
		switch c.Type() {
		case "line_string_content", "multi_line_string_content", "string_content":
			b.WriteString(c.Text())
		}
	}
	if b.Len() == 0 {
		return strings.Trim(n.Text(), `"`)
	}
	return b.String()
}

// authority returns host[:port] of an absolute URL (scheme://host/...), or ""
// when the string is not an absolute URL.
func authority(url string) string {
	i := strings.Index(url, "://")
	if i < 0 {
		return ""
	}
	a := url[i+3:]
	if j := strings.IndexByte(a, '/'); j >= 0 {
		a = a[:j]
	}
	return a
}

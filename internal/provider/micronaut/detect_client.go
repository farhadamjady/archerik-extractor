package micronaut

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
)

// clientDetector extracts outbound HTTP dependencies from Micronaut @Client
// declarative clients. Unlike Spring's Feign (name= vs url= are separate
// attributes), Micronaut's single @Client value is polymorphic: it may be a
// service id ("catalogue"), an absolute URL ("http://host:8080"), a relative
// path ("/pets", the same embedded server), or a ${placeholder}. An explicit
// id= attribute is always a logical service id. TargetName is the RAW logical
// name — never a service_id; the backend maps it (the name-resolution rule).
type clientDetector struct{}

func (clientDetector) Name() string             { return "micronaut.client" }
func (clientDetector) Protocol() model.Protocol { return model.ProtoREST }

// clientQuery captures every @Client annotation, regardless of the interface it
// sits on. Bare @Client (no args) uses the enclosing config — no static target.
const clientQuery = `(annotation
  name: (identifier) @_n
  (#eq? @_n "Client")
) @client`

func (d clientDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: clientQuery, OnMatch: d.onClient}}
}

func (clientDetector) onClient(mc *provider.MatchContext) {
	ann, ok := mc.Captures["client"].(java.Node)
	if !ok || !ann.Valid() {
		return
	}
	base := model.Dependency{Protocol: model.ProtoREST, Detection: model.DetectMicronautClient}

	// id= is always a logical service id (highest precedence when present).
	if id, lit, ok := java.AnnotationNamedValue(ann, "id"); ok {
		base.TargetName = id
		base.Resolved = lit && id != ""
		base.Confidence = confOf(base.Resolved)
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)
		return
	}

	// Otherwise the positional / value= string is the polymorphic target.
	vals, literal, ok := java.AnnotationStringValues(ann, "value")
	if !ok || len(vals) == 0 {
		// Bare @Client — target comes from config/injection; honest uncertain.
		base.Confidence = model.Uncertain
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)
		return
	}
	target := vals[0]

	switch {
	case !literal || strings.Contains(target, "${"):
		// Constant/expression/placeholder — not statically known (config
		// resolution lands in a later round). Uncertain, no target name.
		base.Confidence = model.Uncertain

	case strings.Contains(target, "://"):
		// Absolute URL: target_name is the authority so path variants share a
		// node; the full value is kept in url.
		if host := authority(target); host != "" {
			base.TargetName, base.URL = host, target
			base.Resolved, base.Confidence = true, model.Confirmed
		} else {
			base.URL, base.Confidence = target, model.Uncertain
		}

	case strings.HasPrefix(target, "/"):
		// Relative path — the same embedded server (self/gateway); this names no
		// external service. Keep the path in url, stay anonymous + uncertain.
		base.URL, base.Confidence = target, model.Uncertain

	default:
		// Logical service id (Micronaut service discovery / config client).
		base.TargetName = target
		base.Resolved, base.Confidence = true, model.Confirmed
	}
	mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)
}

func confOf(resolved bool) model.Confidence {
	if resolved {
		return model.Confirmed
	}
	return model.Uncertain
}

// authority returns the host[:port] of an absolute URL, or "" if it is a runtime
// hole. scheme and path are dropped so every path variant shares one node.
func authority(url string) string {
	i := strings.Index(url, "://")
	if i < 0 {
		return ""
	}
	a := url[i+3:]
	if j := strings.IndexByte(a, '/'); j >= 0 {
		a = a[:j]
	}
	if a == "" || strings.Contains(a, "${") {
		return ""
	}
	return a
}

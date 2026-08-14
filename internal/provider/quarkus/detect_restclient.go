package quarkus

import (
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/java"
)

// restClientDetector extracts outbound HTTP dependencies from MicroProfile REST
// Client interfaces (@RegisterRestClient). One edge per interface. The logical
// target is `configKey` (whose URL lives in config as
// quarkus.rest-client.<key>.url — resolved later); an explicit `baseUri`
// hardcodes the URL. With neither, the config key defaults to the interface's
// name, which we emit as the raw logical target (backend maps it, the name-resolution rule).
type restClientDetector struct{}

func (restClientDetector) Name() string             { return "quarkus.restclient" }
func (restClientDetector) Protocol() model.Protocol { return model.ProtoREST }

const registerRestClientQuery = `(annotation
  name: (identifier) @_n
  (#eq? @_n "RegisterRestClient")
) @ann`

// A bare @RegisterRestClient (no arguments) parses as a marker_annotation.
const registerRestClientMarkerQuery = `(marker_annotation
  name: (identifier) @_n
  (#eq? @_n "RegisterRestClient")
) @ann`

func (d restClientDetector) Rules() []provider.Rule {
	return []provider.Rule{
		{Query: registerRestClientQuery, OnMatch: d.onClient},
		{Query: registerRestClientMarkerQuery, OnMatch: d.onClient},
	}
}

func (restClientDetector) onClient(mc *provider.MatchContext) {
	ann, ok := mc.Captures["ann"].(java.Node)
	if !ok || !ann.Valid() {
		return
	}
	base := model.Dependency{Protocol: model.ProtoREST, Detection: model.DetectMPRestClient}

	// baseUri hardcodes the URL (highest precedence for the actual endpoint).
	if uri, literal, ok := java.AnnotationNamedValue(ann, "baseUri"); ok {
		switch {
		case !literal || strings.Contains(uri, "${"):
			base.Confidence = model.Uncertain
		case strings.Contains(uri, "://"):
			if host := authority(uri); host != "" {
				base.TargetName, base.URL, base.Resolved, base.Confidence = host, uri, true, model.Confirmed
			} else {
				base.URL, base.Confidence = uri, model.Uncertain
			}
		default:
			base.URL, base.Confidence = uri, model.Uncertain
		}
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)
		return
	}

	// configKey is the logical name; its URL is resolved from config later.
	if key, literal, ok := java.AnnotationNamedValue(ann, "configKey"); ok && key != "" {
		base.TargetName = key
		base.Resolved = literal
		base.Confidence = confOf(literal)
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)
		return
	}

	// No configKey/baseUri: the config key defaults to the interface FQN. Emit
	// the interface simple name as the raw logical target.
	if iface := enclosingTypeName(ann); iface != "" {
		base.TargetName, base.Resolved, base.Confidence = iface, true, model.Confirmed
	} else {
		base.Confidence = model.Uncertain
	}
	mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)
}

func confOf(resolved bool) model.Confidence {
	if resolved {
		return model.Confirmed
	}
	return model.Uncertain
}

// enclosingTypeName is the simple name of the interface/class the annotation
// sits on.
func enclosingTypeName(ann java.Node) string {
	for p := ann.Parent(); p.Valid(); p = p.Parent() {
		if p.Type() == "interface_declaration" || p.Type() == "class_declaration" {
			return p.ChildByFieldName("name").Text()
		}
	}
	return ""
}

// authority returns host[:port] of an absolute URL, or "" for a runtime hole.
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

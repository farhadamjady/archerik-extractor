package spring

import (
	"fmt"
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/java"
)

// feignDetector extracts outbound HTTP dependencies from @FeignClient interfaces.
//
// TargetName is the raw logical service name (name=/value=/positional) — NOT a
// service_id; the backend maps it (the name-resolution rule). An optional url= overrides
// service discovery; its ${...} placeholders resolve through Index.Config, which
// includes the Helm/K8s/.env deploy layer. When a url exists, edge confidence
// tracks the url (it is what the client actually calls); with no url, it tracks
// the name.
type feignDetector struct{}

func (feignDetector) Name() string             { return "spring.feign" }
func (feignDetector) Protocol() model.Protocol { return model.ProtoREST }

// feignQuery captures every @FeignClient annotation (always argumented — name is
// required), regardless of the interface it sits on.
const feignQuery = `(annotation
  name: (identifier) @_n
  (#eq? @_n "FeignClient")
) @feign`

func (d feignDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: feignQuery, OnMatch: d.onFeign}}
}

func (feignDetector) onFeign(mc *provider.MatchContext) {
	ann, ok := mc.Captures["feign"].(java.Node)
	if !ok || !ann.Valid() {
		return
	}

	// name: positional, name=, or value=.
	nameVals, nameLiteral, hasName := annotationStringValues(ann, "name", "value")
	name := ""
	if hasName && len(nameVals) > 0 {
		name = nameVals[0]
	}

	base := model.Dependency{TargetName: name, Protocol: model.ProtoREST, Detection: model.DetectFeign}

	// url: named-only (never positional).
	urlStr, urlIsStringLit, hasURL := annotationNamedValue(ann, "url")

	if !hasURL {
		// Service-discovery client: the logical name IS the target.
		base.Resolved = hasName && nameLiteral
		base.Confidence = model.Uncertain
		if base.Resolved {
			base.Confidence = model.Confirmed
		}
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)
		return
	}

	switch {
	case !urlIsStringLit:
		// url is an in-code expression (constant/builder); the value resolver
		// will handle these later — unresolved for now, but the name is kept.
		base.Resolved = false
		base.Confidence = model.Uncertain
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)

	case !strings.Contains(urlStr, "${"):
		// Hardcoded url.
		base.URL = urlStr
		base.Resolved = true
		base.Confidence = model.Confirmed
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)

	default:
		// Placeholder url — resolve through config (+ deploy layer).
		emitResolvedURL(mc, base, urlStr, name)
	}
}

// emitResolvedURL resolves a ${...} url to one edge, or one edge per candidate
// when divergent env overlays make the target ambiguous.
func emitResolvedURL(mc *provider.MatchContext, base model.Dependency, urlStr, name string) {
	var cands []provider.ResolvedValue
	if cfg := mc.Index.Config; cfg != nil {
		cands = cfg.Candidates(urlStr)
	}

	switch len(cands) {
	case 0:
		base.Resolved = false
		base.Confidence = model.Uncertain
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)

	case 1:
		base.URL = cands[0].Value
		base.Resolved = true
		base.Confidence = cands[0].Conf
		base.ResolvedVia = cands[0].Source
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)

	default:
		group := fmt.Sprintf("%s:feign:%s", mc.File.Path(), firstNonEmpty(name, urlStr))
		for _, c := range cands {
			d := base
			d.URL = c.Value
			d.Resolved = true
			d.Confidence = model.Likely // one candidate is taken at runtime
			d.ResolvedVia = c.Source
			d.Conditional = true
			d.CandidateGroup = group
			mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, d)
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

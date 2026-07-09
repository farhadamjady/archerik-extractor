package spring

import (
	"fmt"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/resolve"
)

// emitTargets resolves an in-code target expression (a URL argument) via the
// value evaluator and appends outbound dependency edges, shared by the
// RestTemplate and WebClient detectors:
//
//   - Exact, one value  -> one edge, resolved, at the value's confidence.
//   - Exact, many values -> one edge per candidate (conditional/switch/overlay),
//     Conditional + a shared CandidateGroup, capped likely.
//   - Template (holes)  -> one uncertain edge keeping the known shape (http://{?}/x).
//   - Unknown           -> one uncertain edge with no target (an outbound call
//     was made but the endpoint couldn't be pinned) — unresolved deps are still
//     emitted (CLAUDE.md).
func emitTargets(mc *provider.MatchContext, expr java.Node, detection model.DetectionMethod, protocol model.Protocol) {
	vs := resolve.NewUnknown()
	if mc.Resolver != nil {
		vs = mc.Resolver.Resolve(expr)
	}
	base := model.Dependency{Protocol: protocol, Detection: detection}

	switch vs.Kind {
	case resolve.Exact:
		if len(vs.Values) == 1 {
			v := vs.Values[0]
			base.TargetName, base.URL, base.Resolved, base.Confidence = v.S, v.S, true, v.Conf
			mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)
			return
		}
		group := fmt.Sprintf("%s:%d:%s", mc.File.Path(), expr.StartByte(), detection)
		for _, v := range vs.Values {
			d := base
			d.TargetName, d.URL, d.Resolved = v.S, v.S, true
			d.Confidence, d.Conditional, d.CandidateGroup = model.Likely, true, group
			mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, d)
		}

	case resolve.Template:
		base.TargetName = templateString(vs.Segments)
		base.Resolved, base.Confidence = false, model.Uncertain
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)

	default: // Unknown
		base.Resolved, base.Confidence = false, model.Uncertain
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)
	}
}

// templateString renders a template with {?} for each hole,
// e.g. http://{?}/users/{id}.
func templateString(segs []resolve.Segment) string {
	var b strings.Builder
	for _, s := range segs {
		if s.Hole {
			b.WriteString("{?}")
		} else {
			b.WriteString(s.Literal)
		}
	}
	return b.String()
}

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
//   - Template (holes)  -> host known: one resolved edge keeping the shape
//     (http://svc/x/{?}); host a runtime hole: one uncertain edge with the
//     partial shape in url and an EMPTY target_name.
//   - Unknown           -> one uncertain edge with no url and no target_name
//     (an outbound call was made but the endpoint is a runtime value).
//
// target_name is a call TARGET (host / logical service name), never the source
// identifier holding the URL: a variable/bean name like `path` or `client` is
// not a resolvable target, so an unresolvable value stays empty rather than
// polluting the graph with a meaningless node label (the backend renders the
// empty case as a runtime-unknown node). Unresolved deps are still emitted
// (CLAUDE.md); they are just anonymous, which is honest.
func emitTargets(mc *provider.MatchContext, expr java.Node, detection model.DetectionMethod, protocol model.Protocol) {
	group := fmt.Sprintf("%s:%d:%s", mc.File.Path(), expr.StartByte(), detection)
	emitValueSet(mc, resolveNode(mc, expr), detection, protocol, group)
}

// resolveNode evaluates an expression node, tolerating a nil resolver.
func resolveNode(mc *provider.MatchContext, n java.Node) resolve.ValueSet {
	if mc.Resolver == nil {
		return resolve.NewUnknown()
	}
	return mc.Resolver.Resolve(n)
}

// emitValueSet appends the edges for an already-resolved target ValueSet. group
// ties multi-value candidates from one call site together.
func emitValueSet(mc *provider.MatchContext, vs resolve.ValueSet, detection model.DetectionMethod, protocol model.Protocol, group string) {
	base := model.Dependency{Protocol: protocol, Detection: detection}

	switch vs.Kind {
	case resolve.Exact:
		if len(vs.Values) == 1 {
			v := vs.Values[0]
			base.TargetName, base.URL, base.Resolved, base.Confidence = v.S, v.S, true, v.Conf
			mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)
			return
		}
		for _, v := range vs.Values {
			d := base
			d.TargetName, d.URL, d.Resolved = v.S, v.S, true
			d.Confidence, d.Conditional, d.CandidateGroup = model.Likely, true, group
			mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, d)
		}

	case resolve.Template:
		shape := templateString(vs.Segments)
		if templateHostKnown(vs.Segments) {
			// The hole is only in the path/query AFTER a complete host — the
			// TARGET SERVICE is known, like a path variable on an endpoint.
			// (HTTP-only reasoning: a partial Kafka topic stays uncertain,
			// because the topic name itself is the identity.)
			base.TargetName, base.URL = shape, shape
			base.Resolved, base.Confidence = true, model.Likely
		} else {
			// The host itself is (partly) a runtime hole — the target service is
			// unknown. Keep the partial shape in url (so the edge stays distinct
			// and the backend can show it), but leave target_name empty: a
			// runtime host is not a resolvable node label.
			base.URL = shape
			base.Resolved, base.Confidence = false, model.Uncertain
		}
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)

	default: // Unknown
		// A fully runtime value (a variable/bean like `path`) — no url, no
		// target_name. Emitted (never dropped) but anonymous; the backend renders
		// it as a runtime-unknown node rather than inventing a label from the
		// source identifier.
		base.Resolved, base.Confidence = false, model.Uncertain
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)
	}
}

// templateHostKnown reports whether a template's first hole comes only AFTER a
// complete host: the first segment is a literal absolute URL whose path has
// started ("/" after "://"). "http://svc:8080/x/" + {?} -> true;
// "http://" + {?} (host IS the hole) or "http://svc" + {?} (host may continue)
// -> false.
func templateHostKnown(segs []resolve.Segment) bool {
	if len(segs) == 0 || segs[0].Hole {
		return false
	}
	i := strings.Index(segs[0].Literal, "://")
	if i < 0 {
		return false
	}
	return strings.Contains(segs[0].Literal[i+3:], "/")
}

// looksLikeURL reports whether a value set contains an absolute URL (has a
// scheme), used to tell a full-URL uri from a bare path.
func looksLikeURL(vs resolve.ValueSet) bool {
	switch vs.Kind {
	case resolve.Exact:
		for _, v := range vs.Values {
			if strings.Contains(v.S, "://") {
				return true
			}
		}
	case resolve.Template:
		for _, s := range vs.Segments {
			if !s.Hole && strings.Contains(s.Literal, "://") {
				return true
			}
		}
	}
	return false
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

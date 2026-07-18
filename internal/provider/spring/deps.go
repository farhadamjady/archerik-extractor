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
//   - Unknown           -> one uncertain edge labelled with the raw source
//     expression (an outbound call was made but the endpoint couldn't be pinned)
//     — unresolved deps are still emitted (CLAUDE.md), never anonymous.
func emitTargets(mc *provider.MatchContext, expr java.Node, detection model.DetectionMethod, protocol model.Protocol) {
	group := fmt.Sprintf("%s:%d:%s", mc.File.Path(), expr.StartByte(), detection)
	emitValueSet(mc, resolveNode(mc, expr), detection, protocol, group, exprLabel(expr))
}

// exprLabel is the normalized source text of a target expression, used as the
// target_name for an UNRESOLVED edge so it is still identifiable (which variable
// / call was the endpoint) and distinct unresolved call sites don't collapse
// onto one empty "|detection" identity key. Whitespace is collapsed and the
// label capped so it stays a sane node id.
func exprLabel(n java.Node) string {
	s := strings.Join(strings.Fields(n.Text()), " ")
	const max = 120
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// resolveNode evaluates an expression node, tolerating a nil resolver.
func resolveNode(mc *provider.MatchContext, n java.Node) resolve.ValueSet {
	if mc.Resolver == nil {
		return resolve.NewUnknown()
	}
	return mc.Resolver.Resolve(n)
}

// emitValueSet appends the edges for an already-resolved target ValueSet. group
// ties multi-value candidates from one call site together; fallback is the raw
// source-expression label used as target_name when the set is Unknown, so an
// unresolved edge is never anonymous.
func emitValueSet(mc *provider.MatchContext, vs resolve.ValueSet, detection model.DetectionMethod, protocol model.Protocol, group, fallback string) {
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
		base.TargetName = templateString(vs.Segments)
		if templateHostKnown(vs.Segments) {
			// The hole is only in the path/query AFTER a complete host — the
			// TARGET SERVICE is known, like a path variable on an endpoint.
			// (HTTP-only reasoning: a partial Kafka topic stays uncertain,
			// because the topic name itself is the identity.)
			base.URL = base.TargetName
			base.Resolved, base.Confidence = true, model.Likely
		} else {
			base.Resolved, base.Confidence = false, model.Uncertain
		}
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)

	default: // Unknown
		base.TargetName = fallback
		if base.TargetName == "" {
			base.TargetName = "<unresolved>" // never emit an anonymous edge
		}
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

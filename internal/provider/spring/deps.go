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
//   - Exact absolute URL -> one edge, resolved; target_name is the URL's
//     AUTHORITY (host[:port]) so every path variant of one service shares a
//     single backend node, with the full value kept in url.
//   - Exact bare path ("/orders", host supplied by a baseUrl bean elsewhere) ->
//     one anonymous uncertain edge, path in url: this call site names no service.
//   - Exact, many values -> one edge per candidate (conditional/switch/overlay),
//     Conditional + a shared CandidateGroup, capped likely.
//   - Template (holes)  -> host known: one resolved edge, target_name = host,
//     shape (http://svc/x/{?}) in url; host a runtime hole: one uncertain edge
//     with the partial shape in url and an EMPTY target_name.
//   - Unknown           -> one uncertain edge with no url and no target_name
//     (an outbound call was made but the endpoint is a runtime value).
//
// target_name is a call TARGET (host / logical service name), never the source
// identifier holding the URL nor a per-call-site path: a variable/bean name like
// `path`, or a bare "/orders", is not a resolvable target, so it stays empty
// rather than polluting the graph with a meaningless or duplicate node label
// (the backend renders the empty case as a runtime-unknown node). Unresolved
// deps are still emitted; they are just anonymous, which is honest.
func emitTargets(mc *provider.MatchContext, expr java.Node, detection model.DetectionMethod, protocol model.Protocol) {
	group := fmt.Sprintf("%s:%d:%s", mc.File.Path(), expr.StartByte(), detection)
	vs := resolveNode(mc, expr)
	// When the URL names no host (a runtime registry instance host/port), the
	// target service may still be known statically — it is the argument to a
	// service-registry lookup in the same method
	// (EurekaClient.getApplication(name) / getNextServerFromEureka(name)). Emit
	// that logical name as a resolved edge INSTEAD of the anonymous uncertain one
	// emitValueSet would otherwise produce (one edge, not two).
	if hostUnresolved(vs) {
		if name, src, ok := discoveryTarget(mc, expr); ok {
			mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, model.Dependency{
				TargetName: name, Protocol: protocol, Detection: detection,
				Resolved: true, Confidence: model.Likely, ResolvedVia: src,
			})
			return
		}
	}
	emitValueSet(mc, vs, detection, protocol, group)
}

// hostUnresolved reports whether a resolved target value names no host: a fully
// opaque value (Unknown), or a template whose host is itself a hole. These are
// exactly the cases emitValueSet renders as an anonymous uncertain edge, and so
// the only ones a registry-lookup fallback (#37) should try to rescue.
func hostUnresolved(vs resolve.ValueSet) bool {
	switch vs.Kind {
	case resolve.Unknown:
		return true
	case resolve.Template:
		return !templateHostKnown(vs.Segments)
	default:
		return false
	}
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
			if host, ok := targetHost(v.S); ok {
				base.TargetName, base.URL, base.Resolved, base.Confidence = host, v.S, true, v.Conf
				base.ResolvedVia = v.Source
			} else {
				// Bare path / opaque value: an outbound call, but this site names
				// no service — keep it in url, stay anonymous and uncertain.
				base.URL, base.Resolved, base.Confidence = v.S, false, model.Uncertain
			}
			mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)
			return
		}
		for _, v := range vs.Values {
			d := base
			d.URL, d.Conditional, d.CandidateGroup = v.S, true, group
			if host, ok := targetHost(v.S); ok {
				d.TargetName, d.Resolved, d.Confidence = host, true, model.Likely
				d.ResolvedVia = v.Source
			} else {
				d.Resolved, d.Confidence = false, model.Uncertain
			}
			mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, d)
		}

	case resolve.Template:
		shape := templateString(vs.Segments)
		if templateHostKnown(vs.Segments) {
			// The hole is only in the path/query AFTER a complete host — the
			// TARGET SERVICE is known, like a path variable on an endpoint.
			// (HTTP-only reasoning: a partial Kafka topic stays uncertain,
			// because the topic name itself is the identity.) target_name is the
			// host alone; the templated path stays in url.
			host, _ := targetHost(shape) // host is complete before the first hole
			base.TargetName, base.URL = host, shape
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

// targetHost derives the target_name for a resolved HTTP endpoint value. An
// absolute URL collapses to its AUTHORITY (host[:port], scheme and path
// dropped) so all path variants of one service share a node; the raw host casing
// is preserved (the backend lowercases/strips the port when mapping name ->
// service_id). ok=false means the value carries no resolvable target:
//   - a bare path ("/orders") — the host lives elsewhere (a baseUrl bean), so
//     this call site alone names no service;
//   - an absolute URL whose authority is or contains a runtime hole.
//
// A non-URL, non-path value (a logical host like "payment-service" resolved from
// a property) is returned as-is.
func targetHost(value string) (host string, ok bool) {
	if i := strings.Index(value, "://"); i >= 0 {
		authority := value[i+3:]
		if j := strings.IndexByte(authority, '/'); j >= 0 {
			authority = authority[:j]
		}
		if authority == "" || strings.Contains(authority, "{?}") {
			return "", false // host itself is a runtime hole
		}
		return authority, true
	}
	if strings.HasPrefix(value, "/") {
		return "", false // bare path — host supplied elsewhere
	}
	return value, true
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

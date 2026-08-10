package spring

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
)

// httpExchangeDetector extracts outbound HTTP dependencies from Spring 6
// declarative clients: interfaces with @HttpExchange / @GetExchange /
// @PostExchange / ... methods. Same shape as Feign: one edge per interface;
// the target is the interface-level @HttpExchange url (resolved via config) or
// the interface name as the raw logical name — the backend maps names, like
// Feign names.
type httpExchangeDetector struct{}

func (httpExchangeDetector) Name() string             { return "spring.httpexchange" }
func (httpExchangeDetector) Protocol() model.Protocol { return model.ProtoREST }

const httpExchangeQuery = `(annotation
  name: (identifier) @_n
  (#match? @_n "^(HttpExchange|GetExchange|PostExchange|PutExchange|DeleteExchange|PatchExchange)$")
) @ann`

// Also match the no-argument marker form (@GetExchange without a path).
const httpExchangeMarkerQuery = `(marker_annotation
  name: (identifier) @_n
  (#match? @_n "^(HttpExchange|GetExchange|PostExchange|PutExchange|DeleteExchange|PatchExchange)$")
) @ann`

func (d httpExchangeDetector) Rules() []provider.Rule {
	return []provider.Rule{
		{Query: httpExchangeQuery, OnMatch: d.onExchange},
		{Query: httpExchangeMarkerQuery, OnMatch: d.onExchange},
	}
}

// onExchange fires once per exchange annotation; identical edges from the
// methods of one interface collapse in the marshal dedup.
func (httpExchangeDetector) onExchange(mc *provider.MatchContext) {
	ann, _ := mc.Captures["ann"].(java.Node)
	iface := enclosingInterface(ann)
	if !iface.Valid() {
		return // exchange annotations only make a CLIENT on an interface
	}

	base := model.Dependency{
		TargetName: iface.ChildByFieldName("name").Text(),
		Protocol:   model.ProtoREST,
		Detection:  model.DetectHTTPExchange,
	}

	// Interface-level @HttpExchange may carry the base url.
	if mods := childByType(iface, "modifiers"); mods.Valid() {
		if ex := findAnnotation(mods, "HttpExchange"); ex.Valid() {
			if urls, literal, ok := annotationStringValues(ex, "url", "value"); ok && len(urls) > 0 && literal {
				emitExchangeURL(mc, base, urls[0])
				return
			}
		}
	}
	// No base url: emit the logical name only (like a name-only Feign client).
	base.Resolved, base.Confidence = true, model.Confirmed
	mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)
}

func emitExchangeURL(mc *provider.MatchContext, base model.Dependency, url string) {
	if !strings.Contains(url, "${") {
		base.URL, base.Resolved, base.Confidence = url, true, model.Confirmed
		mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)
		return
	}
	emitResolvedURL(mc, base, url, base.TargetName) // reuse the Feign path
}

func enclosingInterface(n java.Node) java.Node {
	for p := n.Parent(); p.Valid(); p = p.Parent() {
		if p.Type() == "interface_declaration" {
			return p
		}
		if p.Type() == "class_declaration" {
			return java.Node{}
		}
	}
	return java.Node{}
}

package quarkus

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/resolve"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// messagingDetector extracts Kafka edges from MicroProfile Reactive Messaging:
// @Incoming(ch) methods (consumers), @Outgoing(ch) methods (producers), and
// @Channel(ch) injected Emitters (producers). A "channel" is a logical name; the
// Kafka topic and the connector live in application.properties
// (mp.messaging.<dir>.<ch>.topic — defaults to the channel name — and .connector).
// An edge is emitted ONLY when the channel's connector is a Kafka connector, so
// SmallRye in-memory channels (used here for WebSocket fan-out) never become
// false Kafka topics. Channel names may be constants, resolved via the evaluator.
type messagingDetector struct{}

func (messagingDetector) Name() string             { return "quarkus.messaging" }
func (messagingDetector) Protocol() model.Protocol { return model.ProtoKafka }

const (
	incomingQuery = `(annotation
  name: (identifier) @_n (#eq? @_n "Incoming")
) @ann`
	outgoingQuery = `(annotation
  name: (identifier) @_n (#eq? @_n "Outgoing")
) @ann`
	channelQuery = `(annotation
  name: (identifier) @_n (#eq? @_n "Channel")
) @ann`
)

func (d messagingDetector) Rules() []provider.Rule {
	return []provider.Rule{
		{Query: incomingQuery, OnMatch: d.onIncoming},
		{Query: outgoingQuery, OnMatch: d.onOutgoing},
		{Query: channelQuery, OnMatch: d.onChannel},
	}
}

func (d messagingDetector) onIncoming(mc *provider.MatchContext) {
	d.emit(mc, false, "incoming")
}

func (d messagingDetector) onOutgoing(mc *provider.MatchContext) {
	d.emit(mc, true, "outgoing")
}

// onChannel handles @Channel on an injected Emitter/MutinyEmitter (a producer).
// @Channel also appears on a method-return / consumer Multi, but the common,
// unambiguous producer form is the Emitter injection; treat @Channel as outgoing.
func (d messagingDetector) onChannel(mc *provider.MatchContext) {
	d.emit(mc, true, "outgoing")
}

// emit resolves the channel name, maps it to a Kafka topic through config, and
// appends the producer/consumer edge — but only for Kafka-connector channels.
func (d messagingDetector) emit(mc *provider.MatchContext, producer bool, dir string) {
	ann, ok := mc.Captures["ann"].(java.Node)
	if !ok || !ann.Valid() {
		return
	}
	channel, ok := channelName(mc, ann)
	if !ok || channel == "" {
		return
	}
	cfg, ok := mc.Index.Config.(*flatConfig)
	if !ok {
		return // no config parsed — cannot tell Kafka from in-memory, stay silent
	}
	if !isKafkaChannel(cfg, dir, channel) {
		return // in-memory / non-Kafka connector — not a Kafka topic
	}
	topic := channel
	if t, ok := cfg.Lookup("mp.messaging." + dir + "." + channel + ".topic"); ok && t != "" {
		topic = t
	}
	sch := messagingSchema(mc, ann, producer)

	edge := model.KafkaEdge{
		Topic:      topic,
		Resolved:   true,
		Protocol:   model.ProtoKafka,
		Detection:  model.DetectReactiveMessaging,
		Confidence: model.Likely, // channel→topic is one config indirection
		Schema:     sch,
	}
	if producer {
		mc.Out.KafkaProducers = append(mc.Out.KafkaProducers, edge)
	} else {
		mc.Out.KafkaConsumers = append(mc.Out.KafkaConsumers, edge)
	}
}

// channelName resolves the annotation's channel value from its single argument —
// a string literal, or a constant reference (positional OR value=, often a
// `static final String CH = "fights"`) folded by the evaluator.
func channelName(mc *provider.MatchContext, ann java.Node) (string, bool) {
	node := annotationArgNode(ann)
	if !node.Valid() {
		return "", false
	}
	if node.Type() == "string_literal" {
		return java.Unquote(node.Text()), true
	}
	if mc.Resolver != nil {
		vs := mc.Resolver.Resolve(node)
		if vs.Kind == resolve.Exact && len(vs.Values) == 1 {
			return vs.Values[0].S, true
		}
	}
	return "", false
}

// annotationArgNode returns the single value node of a channel annotation: the
// positional argument, or the value= argument. @Incoming/@Outgoing/@Channel each
// take exactly one channel name, so there is no array to unwrap.
func annotationArgNode(ann java.Node) java.Node {
	args := ann.ChildByFieldName("arguments")
	if !args.Valid() {
		return java.Node{}
	}
	for _, c := range java.NamedChildren(args) {
		if c.Type() == "element_value_pair" {
			if c.ChildByFieldName("key").Text() == "value" {
				return c.ChildByFieldName("value")
			}
			continue
		}
		return c // positional argument
	}
	return java.Node{}
}

// isKafkaChannel reports whether the channel is backed by a Kafka connector:
// its .connector is a smallrye-kafka connector, or (connector absent) it carries
// Kafka-specific keys (bootstrap.servers). No such evidence → treat as in-memory.
func isKafkaChannel(cfg *flatConfig, dir, channel string) bool {
	base := "mp.messaging." + dir + "." + channel + "."
	if conn, ok := cfg.Lookup(base + "connector"); ok {
		return strings.Contains(conn, "kafka")
	}
	if _, ok := cfg.Lookup(base + "bootstrap.servers"); ok {
		return true
	}
	return false
}

// messagingSchema resolves the payload type: the @Incoming method's non-metadata
// parameter, or the @Outgoing method's return / the @Channel Emitter's generic.
func messagingSchema(mc *provider.MatchContext, ann java.Node, producer bool) *model.Schema {
	pt := ""
	if method := enclosingOfTypes(ann, "method_declaration"); method.Valid() {
		if producer {
			pt = unwrapReactive(returnType(method))
		} else {
			pt = incomingPayloadType(method)
		}
	}
	if pt == "" {
		pt = emitterPayloadType(ann) // @Channel Emitter<T> field/param
	}
	if pt == "" {
		return nil
	}
	return schema.ResolveKafka(unwrapMessage(pt), mc.Index.Schemas, mc.Index.Types)
}

func returnType(method java.Node) string {
	if t := method.ChildByFieldName("type"); t.Valid() {
		return t.Text()
	}
	return ""
}

// incomingPayloadType is the first non-metadata parameter type of a consumer.
func incomingPayloadType(method java.Node) string {
	params := java.ChildByType(method, "formal_parameters")
	if !params.Valid() {
		return ""
	}
	for _, p := range java.NamedChildren(params) {
		if p.Type() == "formal_parameter" {
			return unwrapMessage(p.ChildByFieldName("type").Text())
		}
	}
	return ""
}

// emitterPayloadType reads T from an Emitter<T>/MutinyEmitter<T> declaration the
// @Channel annotation sits on (a field or constructor parameter).
func emitterPayloadType(ann java.Node) string {
	decl := enclosingOfTypes(ann, "formal_parameter", "field_declaration")
	if !decl.Valid() {
		return ""
	}
	return emitterInner(decl.ChildByFieldName("type").Text())
}

func emitterInner(t string) string {
	for _, w := range []string{"Emitter", "MutinyEmitter"} {
		if i := strings.Index(t, w+"<"); i >= 0 {
			inner := t[i+len(w)+1:]
			if j := strings.LastIndex(inner, ">"); j >= 0 {
				return strings.TrimSpace(inner[:j])
			}
		}
	}
	return ""
}

// unwrapMessage strips a Message<T>/reactive wrapper to the payload type T.
func unwrapMessage(t string) string {
	t = unwrapReactive(t)
	for _, w := range []string{"Message", "Record", "ConsumerRecord", "ProducerRecord"} {
		if strings.HasPrefix(t, w+"<") && strings.HasSuffix(t, ">") {
			inner := strings.TrimSpace(t[len(w)+1 : len(t)-1])
			// Record<K,V>/ConsumerRecord<K,V> — the payload is the last arg.
			if i := strings.LastIndex(inner, ","); i >= 0 {
				return strings.TrimSpace(inner[i+1:])
			}
			return inner
		}
	}
	return t
}

// enclosingOfTypes returns the nearest ancestor of n whose type is one of types.
func enclosingOfTypes(n java.Node, types ...string) java.Node {
	for p := n.Parent(); p.Valid(); p = p.Parent() {
		for _, t := range types {
			if p.Type() == t {
				return p
			}
		}
	}
	return java.Node{}
}

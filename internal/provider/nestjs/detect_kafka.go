package nestjs

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// kafkaDetector extracts Kafka edges from NestJS/Node services (K8, topics only —
// payload schema is K9). Two idioms are covered:
//
//   - NestJS microservice handlers: @MessagePattern('t') / @EventPattern('t') on
//     a controller method -> a consumed topic; the injected ClientKafka/ClientProxy
//     producer this.client.emit('t', payload) -> a produced topic.
//   - Raw kafkajs: producer.send({ topic: 't', ... }) -> produced;
//     consumer.subscribe({ topic: 't' } / { topics: ['a','b'] }) -> consumed.
//
// The object-literal forms (kafkajs) are unambiguous. The bare-string emit() form
// is gated on the file importing @nestjs/microservices, so a generic .emit() (an
// EventEmitter) is not mistaken for a Kafka producer. The ambiguous string-form
// ClientKafka.send('t', …) (request/response) is intentionally not matched — it
// collides with res.send(...); kafkajs producers use the object form, which is
// matched. A literal topic is confirmed; a computed one is an honest uncertain
// edge, never dropped.
type kafkaDetector struct{}

func (kafkaDetector) Name() string             { return "nestjs.kafka" }
func (kafkaDetector) Protocol() model.Protocol { return model.ProtoKafka }

const (
	// every class; the handler walks methods for @MessagePattern/@EventPattern.
	kafkaHandlerQuery = `(class_declaration) @class`
	// every `<obj>.<method>(<args>)` call; the handler filters to send/subscribe/emit.
	kafkaCallQuery = `(call_expression
  function: (member_expression
    object: (_) @obj
    property: (property_identifier) @method)
  arguments: (arguments) @args) @call`
)

func (d kafkaDetector) Rules() []provider.Rule {
	return []provider.Rule{
		{Query: kafkaHandlerQuery, OnMatch: d.onClass},
		{Query: kafkaCallQuery, OnMatch: d.onCall},
	}
}

// onClass emits a consumed-topic edge for each @MessagePattern/@EventPattern
// method handler in a class.
func (kafkaDetector) onClass(mc *provider.MatchContext) {
	class, ok := mc.Captures["class"].(tsjs.Node)
	if !ok || !class.Valid() {
		return
	}
	body := tsjs.ChildByType(class, "class_body")
	if !body.Valid() {
		return
	}
	for _, m := range tsjs.NamedChildren(body) {
		if m.Type() != "method_definition" {
			continue
		}
		for _, dec := range tsjs.PrecedingDecorators(m) {
			switch tsjs.DecoratorName(dec) {
			case "MessagePattern", "EventPattern":
				if v, literal, ok := tsjs.DecoratorStringArg(dec); ok {
					emitKafkaTopic(mc, topicVal{v, literal}, false, consumerPayloadSchema(mc, m))
				}
			}
		}
	}
}

// consumerPayloadSchema resolves a message handler's payload schema (K9): the
// type of the @Payload()-decorated parameter, or the first typed non-@Ctx
// parameter, resolved files-first via schema.ResolveKafka. Returns nil when no
// payload type is found or it doesn't resolve (edge kept, schema dropped).
func consumerPayloadSchema(mc *provider.MatchContext, method tsjs.Node) *model.Schema {
	pt := payloadParamType(method)
	if pt == "" {
		return nil
	}
	return schema.ResolveKafka(pt, mc.Index.Schemas, mc.Index.Types)
}

// payloadParamType returns the payload parameter's declared type: the
// @Payload()-decorated parameter's type wins; otherwise the first typed
// parameter that is not @Ctx-decorated (the by-convention message argument).
func payloadParamType(method tsjs.Node) string {
	params := method.ChildByFieldName("parameters")
	if !params.Valid() {
		return ""
	}
	firstTyped := ""
	for _, p := range tsjs.NamedChildren(params) {
		ta := p.ChildByFieldName("type")
		isPayload, isCtx := false, false
		for _, d := range tsjs.NamedChildren(p) {
			if d.Type() != "decorator" {
				continue
			}
			switch tsjs.DecoratorName(d) {
			case "Payload":
				isPayload = true
			case "Ctx":
				isCtx = true
			}
		}
		if isPayload && ta.Valid() {
			return normalizeType(typeText(ta))
		}
		if !isCtx && ta.Valid() && firstTyped == "" {
			firstTyped = normalizeType(typeText(ta))
		}
	}
	return firstTyped
}

// onCall handles the call-based producer/consumer idioms.
func (kafkaDetector) onCall(mc *provider.MatchContext) {
	method, _ := mc.Captures["method"].(tsjs.Node)
	args, _ := mc.Captures["args"].(tsjs.Node)
	if !method.Valid() || !args.Valid() {
		return
	}
	kids := tsjs.NamedChildren(args)
	if len(kids) == 0 {
		return
	}
	switch method.Text() {
	case "send": // kafkajs producer.send({ topic, ... })
		for _, tv := range objectTopics(kids[0]) {
			emitKafkaTopic(mc, tv, true, nil)
		}
	case "subscribe": // kafkajs consumer.subscribe({ topic } / { topics: [...] })
		for _, tv := range objectTopics(kids[0]) {
			emitKafkaTopic(mc, tv, false, nil)
		}
	case "emit": // ClientKafka.emit('pattern', payload) — gated to avoid EventEmitter
		if !fileImportsMicroservices(mc.File) {
			return
		}
		if tv, ok := stringPattern(kids[0]); ok {
			emitKafkaTopic(mc, tv, true, nil)
		}
	}
}

// topicVal is a resolved topic and whether it came from a literal.
type topicVal struct {
	value   string
	literal bool
}

// objectTopics reads the topic(s) from a kafkajs options object: `{ topic: 't' }`
// (one) or `{ topics: ['a', 'b'] }` (many). Returns nil when the argument is not
// an object carrying a topic/topics key. A non-string topic value is kept as an
// uncertain (non-literal) topic.
func objectTopics(arg tsjs.Node) []topicVal {
	if arg.Type() != "object" {
		return nil
	}
	var out []topicVal
	for _, p := range tsjs.NamedChildren(arg) {
		if p.Type() != "pair" {
			continue
		}
		key := tsjs.ChildByType(p, "property_identifier")
		if !key.Valid() {
			continue
		}
		val := p.ChildByFieldName("value")
		switch key.Text() {
		case "topic":
			if tv, ok := stringPattern(val); ok {
				out = append(out, tv)
			}
		case "topics":
			if val.Type() == "array" {
				for _, e := range tsjs.NamedChildren(val) {
					if tv, ok := stringPattern(e); ok {
						out = append(out, tv)
					}
				}
			}
		}
	}
	return out
}

// stringPattern reads a topic/pattern expression: a string literal (confirmed) or
// any other expression (identifier/template — uncertain). Returns ok=false for
// nodes that don't carry a usable value.
func stringPattern(n tsjs.Node) (topicVal, bool) {
	switch n.Type() {
	case "string":
		return topicVal{tsjs.StringValue(n), true}, true
	case "identifier", "template_string", "member_expression":
		return topicVal{n.Text(), false}, true
	}
	return topicVal{}, false
}

// emitKafkaTopic appends one topic edge (producer or consumer) with an optional
// payload schema (nil when unresolved). A literal topic is confirmed and
// resolved; a computed one is an honest uncertain edge.
func emitKafkaTopic(mc *provider.MatchContext, tv topicVal, producer bool, sch *model.Schema) {
	edge := model.KafkaEdge{Protocol: model.ProtoKafka, Detection: model.DetectKafka, Schema: sch}
	if tv.literal {
		edge.Topic, edge.Resolved, edge.Confidence = tv.value, true, model.Confirmed
	} else {
		edge.Topic, edge.Resolved, edge.Confidence = tv.value, false, model.Uncertain
	}
	if producer {
		mc.Out.KafkaProducers = append(mc.Out.KafkaProducers, edge)
	} else {
		mc.Out.KafkaConsumers = append(mc.Out.KafkaConsumers, edge)
	}
}

// fileImportsMicroservices reports whether the file imports @nestjs/microservices
// — the gate that keeps a generic .emit() (EventEmitter) from being read as a
// ClientKafka producer.
func fileImportsMicroservices(f provider.ParsedFile) bool {
	jf, ok := f.(*tsjs.File)
	return ok && strings.Contains(string(jf.Src()), "@nestjs/microservices")
}

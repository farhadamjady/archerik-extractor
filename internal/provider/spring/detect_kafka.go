package spring

import (
	"fmt"
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/java"
	"github.com/farhadamjady/archerik-extractor/internal/resolve"
	"github.com/farhadamjady/archerik-extractor/internal/schema"
)

// kafkaDetector extracts Kafka edges: producers from KafkaTemplate.send call
// sites, consumers from @KafkaListener methods. The producer/consumer -> topic
// edge is ALWAYS emitted when the call is real, independent of whether the topic
// (or a payload schema) resolves — an unresolved topic yields an
// uncertain edge, never a dropped one.
type kafkaDetector struct{}

func (kafkaDetector) Name() string             { return "spring.kafka" }
func (kafkaDetector) Protocol() model.Protocol { return model.ProtoKafka }

const (
	kafkaProducerQuery = `(method_invocation
  name: (identifier) @name
  arguments: (argument_list) @args
) @call`
	kafkaConsumerQuery = `(annotation
  name: (identifier) @_n
  (#eq? @_n "KafkaListener")
) @ann`
)

func (d kafkaDetector) Rules() []provider.Rule {
	return []provider.Rule{
		{Query: kafkaProducerQuery, OnMatch: d.onInvocation},
		{Query: kafkaConsumerQuery, OnMatch: d.onConsumer},
	}
}

// onInvocation dispatches call sites: KafkaTemplate.send -> producer; Kafka
// Streams builder.stream/table/globalTable -> consumer and KStream.to ->
// producer. The Streams cases only fire in files that import
// org.apache.kafka.streams, so List.stream() and unrelated to() never match.
func (kafkaDetector) onInvocation(mc *provider.MatchContext) {
	name, _ := mc.Captures["name"].(java.Node)
	call, _ := mc.Captures["call"].(java.Node)
	args, _ := mc.Captures["args"].(java.Node)
	topic := args.NamedChild(0)

	switch name.Text() {
	case "send":
		valueType, ok := kafkaTemplateValueType(call, receiverName(call))
		if !ok || !topic.Valid() {
			return
		}
		group := fmt.Sprintf("%s:%d:kafka-p", mc.File.Path(), topic.StartByte())
		emitKafkaTopic(mc, producerTopic(mc, args), true, group, kafkaSchema(mc, valueType))

	case "stream", "table", "globalTable":
		if !usesKafkaStreams(mc.File) || !topic.Valid() {
			return
		}
		group := fmt.Sprintf("%s:%d:kafka-sc", mc.File.Path(), topic.StartByte())
		emitKafkaTopic(mc, resolveNode(mc, topic), false, group, nil)

	case "to":
		if !usesKafkaStreams(mc.File) || !topic.Valid() {
			return
		}
		group := fmt.Sprintf("%s:%d:kafka-sp", mc.File.Path(), topic.StartByte())
		emitKafkaTopic(mc, resolveNode(mc, topic), true, group, nil)

	case "aggregateType", "setAggregateType":
		// Debezium outbox producer: the service writes an OutBox row whose aggregate
		// type routes the topic in the connector's EventRouter config — set through
		// the builder (`.aggregateType(X)`) or the setter
		// (`outbox.setAggregateType(X)`). Gated on outbox connector JSONs existing
		// in the repo, so unrelated methods never fire.
		if len(mc.Index.OutboxRoutes) == 0 || !topic.Valid() || args.NamedChildCount() != 1 {
			return
		}
		argVS := resolveNode(mc, topic)
		for i, route := range mc.Index.OutboxRoutes {
			group := fmt.Sprintf("%s:%d:kafka-ob%d", mc.File.Path(), topic.StartByte(), i)
			emitKafkaTopic(mc, capTopicLikely(outboxTopic(route, argVS)), true, group, nil)
		}
	}
}

// outboxTopic instantiates an EventRouter route pattern with the aggregate-type
// value: "${routedByValue}.events" + ORDER -> ORDER.events. An unresolved
// aggregate type flows through as a hole (template -> uncertain edge).
func outboxTopic(route string, aggType resolve.ValueSet) resolve.ValueSet {
	prefix, suffix, found := strings.Cut(route, "${routedByValue}")
	if !found {
		return resolve.NewExact(model.Confirmed, route)
	}
	vs := resolve.NewExact(model.Confirmed, prefix)
	vs = resolve.Concat(vs, aggType)
	return resolve.Concat(vs, resolve.NewExact(model.Confirmed, suffix))
}

// usesKafkaStreams reports whether the file imports the Kafka Streams API.
func usesKafkaStreams(f provider.ParsedFile) bool {
	jf, ok := f.(*java.File)
	return ok && strings.Contains(string(jf.Src()), "org.apache.kafka.streams")
}

// onConsumer emits a consumed-topic edge per topic on a @KafkaListener, with the
// message payload's schema attached.
func (kafkaDetector) onConsumer(mc *provider.MatchContext) {
	ann, _ := mc.Captures["ann"].(java.Node)
	sch := kafkaSchema(mc, consumerPayloadType(ann))
	for _, node := range annotationValueNodes(ann, "topics") {
		group := fmt.Sprintf("%s:%d:kafka-c", mc.File.Path(), node.StartByte())
		emitKafkaTopic(mc, resolveTopicNode(mc, node), false, group, sch)
	}
}

// kafkaSchema resolves a payload type's schema files-first (safe-fail: nil keeps
// the edge, drops only the schema).
func kafkaSchema(mc *provider.MatchContext, payloadType string) *model.Schema {
	if payloadType == "" {
		return nil
	}
	return schema.ResolveKafka(payloadType, mc.Index.Schemas, mc.Index.Types)
}

// consumerPayloadType is the @KafkaListener method's payload parameter type
// (the first parameter that is not a @Header), or "".
func consumerPayloadType(ann java.Node) string {
	m := enclosingOfTypes(ann, "method_declaration")
	if !m.Valid() {
		return ""
	}
	params := childByType(m, "formal_parameters")
	for _, p := range namedChildren(params) {
		if p.Type() != "formal_parameter" {
			continue
		}
		if mods := childByType(p, "modifiers"); mods.Valid() && findAnnotation(mods, "Header").Valid() {
			continue
		}
		return p.ChildByFieldName("type").Text()
	}
	return ""
}

// resolveTopicNode resolves a @KafkaListener topic value. A string literal with
// a ${...} placeholder is resolved through config (Spring resolves annotation
// placeholders); a plain literal is taken as-is; anything else (a constant) goes
// through the value evaluator.
func resolveTopicNode(mc *provider.MatchContext, node java.Node) resolve.ValueSet {
	if node.Type() == "string_literal" {
		s := unquote(node.Text())
		if strings.Contains(s, "${") {
			if cfg := mc.Index.Config; cfg != nil {
				return resolvedValuesToVS(cfg.Candidates(s))
			}
			return resolve.NewUnknown()
		}
		return resolve.NewExact(model.Confirmed, s)
	}
	return resolveNode(mc, node)
}

func resolvedValuesToVS(cands []provider.ResolvedValue) resolve.ValueSet {
	if len(cands) == 0 {
		return resolve.NewUnknown()
	}
	vals := make([]resolve.Value, len(cands))
	for i, c := range cands {
		vals[i] = resolve.Value{S: c.Value, Conf: c.Conf, Source: c.Source}
	}
	return resolve.ExactValues(vals...)
}

// producerTopic resolves the destination topic of a KafkaTemplate.send(...)
// call across its overloads. send(topic, data) / send(topic, key, data) carry
// the topic as the first argument (resolved as before). send(Message) hides
// it: the idiomatic Spring form sets the topic in a KafkaHeaders.TOPIC header,
// usually `topic.name()` on an injected NewTopic bean. An unresolvable topic
// stays Unknown — an honest uncertain edge, never dropped.
func producerTopic(mc *provider.MatchContext, args java.Node) resolve.ValueSet {
	arg0 := args.NamedChild(0)
	if args.NamedChildCount() >= 2 {
		return resolveNode(mc, arg0) // topic-first overloads
	}
	if expr := messageTopicExpr(arg0); expr.Valid() {
		return resolveTopicExpr(mc, expr)
	}
	return resolve.NewUnknown() // Message/ProducerRecord we can't trace
}

// messageTopicExpr finds the KafkaHeaders.TOPIC header expression for a single
// Message argument to send(): the argument is either the MessageBuilder chain
// itself or a local variable holding it.
func messageTopicExpr(arg java.Node) java.Node {
	chain := arg
	if arg.Type() == "identifier" {
		chain = localVarInit(arg, arg.Text())
	}
	if !chain.Valid() {
		return java.Node{}
	}
	return topicHeaderArg(chain)
}

// localVarInit returns the initializer expression of a local variable `name`
// declared before `use` in the same method (last such definition wins).
func localVarInit(use java.Node, name string) java.Node {
	method := enclosingOfTypes(use, "method_declaration", "constructor_declaration")
	if !method.Valid() {
		return java.Node{}
	}
	before := use.StartByte()
	var val java.Node
	method.Walk(func(m java.Node) bool {
		if m.Type() != "local_variable_declaration" {
			return true
		}
		for _, d := range namedChildren(m) {
			if d.Type() == "variable_declarator" && d.ChildByFieldName("name").Text() == name {
				if v := d.ChildByFieldName("value"); v.Valid() && v.StartByte() < before {
					val = v
				}
			}
		}
		return true
	})
	return val
}

// topicHeaderArg scans a MessageBuilder chain for
// .setHeader(KafkaHeaders.TOPIC, <expr>) and returns <expr>.
func topicHeaderArg(chain java.Node) java.Node {
	var out java.Node
	chain.Walk(func(n java.Node) bool {
		if out.Valid() {
			return false
		}
		if n.Type() == "method_invocation" && n.ChildByFieldName("name").Text() == "setHeader" {
			a := n.ChildByFieldName("arguments")
			if isKafkaTopicHeaderKey(a.NamedChild(0)) {
				if v := a.NamedChild(1); v.Valid() {
					out = v
				}
			}
		}
		return true
	})
	return out
}

// isKafkaTopicHeaderKey matches the header key that names the destination topic:
// the KafkaHeaders.TOPIC constant or its literal value "kafka_topic".
func isKafkaTopicHeaderKey(n java.Node) bool {
	switch n.Type() {
	case "field_access":
		return n.ChildByFieldName("object").Text() == "KafkaHeaders" &&
			n.ChildByFieldName("field").Text() == "TOPIC"
	case "string_literal":
		return unquote(n.Text()) == "kafka_topic"
	}
	return false
}

// resolveTopicExpr resolves a topic header value. `<newTopic>.name()` on an
// injected NewTopic bean resolves through the indexed TopicBeans (and thence the
// config layer); anything else goes through the ordinary value evaluator.
func resolveTopicExpr(mc *provider.MatchContext, expr java.Node) resolve.ValueSet {
	if expr.Type() == "method_invocation" &&
		expr.ChildByFieldName("name").Text() == "name" &&
		!expr.ChildByFieldName("arguments").NamedChild(0).Valid() &&
		receiverIsNewTopic(expr, expr.ChildByFieldName("object")) {
		if vs, ok := resolveTopicBean(mc); ok {
			return vs
		}
		return resolve.NewUnknown()
	}
	return resolveNode(mc, expr)
}

// resolveTopicBean resolves the topic name(s) declared by the service's Kafka
// NewTopic bean(s) through the config layer, capped at likely (the value reaches
// the send call through a bean + header indirection). Multiple beans union into
// conditional candidates.
func resolveTopicBean(mc *provider.MatchContext) (resolve.ValueSet, bool) {
	if mc.Index == nil || len(mc.Index.TopicBeans) == 0 {
		return resolve.ValueSet{}, false
	}
	result := resolve.NewUnknown()
	for _, b := range mc.Index.TopicBeans {
		if arg, ok := b.NameArg.(java.Node); ok {
			result = resolve.Union(result, resolveNode(mc, arg))
		}
	}
	if result.Kind == resolve.Unknown {
		return resolve.ValueSet{}, false
	}
	return capTopicLikely(result), true
}

// capTopicLikely lowers confirmed values to likely: a topic reached through a
// NewTopic bean and a Message header is one indirection removed from a literal.
func capTopicLikely(vs resolve.ValueSet) resolve.ValueSet {
	if vs.Kind != resolve.Exact {
		return vs
	}
	vals := make([]resolve.Value, len(vs.Values))
	for i, v := range vs.Values {
		if v.Conf == model.Confirmed {
			v.Conf = model.Likely
		}
		vals[i] = v
	}
	return resolve.ExactValues(vals...)
}

// receiverIsNewTopic reports whether `recv` (an identifier or this.field) is
// declared with type NewTopic in the enclosing method's parameters or class
// fields — the guard that keeps `x.name()` from matching non-topic receivers.
func receiverIsNewTopic(ctx, recv java.Node) bool {
	name := recv.Text()
	if recv.Type() == "field_access" && recv.ChildByFieldName("object").Text() == "this" {
		name = recv.ChildByFieldName("field").Text()
	}
	if m := enclosingOfTypes(ctx, "method_declaration", "constructor_declaration"); m.Valid() {
		if params := childByType(m, "formal_parameters"); params.Valid() {
			for _, p := range namedChildren(params) {
				if p.Type() == "formal_parameter" && p.ChildByFieldName("name").Text() == name {
					return isNewTopicType(p.ChildByFieldName("type").Text())
				}
			}
		}
	}
	if cls := enclosingOfTypes(ctx, "class_declaration"); cls.Valid() {
		body := cls.ChildByFieldName("body")
		for _, fd := range namedChildren(body) {
			if fd.Type() != "field_declaration" {
				continue
			}
			for _, d := range namedChildren(fd) {
				if d.Type() == "variable_declarator" && d.ChildByFieldName("name").Text() == name {
					return isNewTopicType(fd.ChildByFieldName("type").Text())
				}
			}
		}
	}
	return false
}

// emitKafkaTopic appends a topic edge to the producer or consumer slice. Like
// emitValueSet but for KafkaEdge; the edge is emitted for every kind, including
// Unknown (topic unresolved but the producer/consumer is real).
func emitKafkaTopic(mc *provider.MatchContext, vs resolve.ValueSet, producer bool, group string, sch *model.Schema) {
	edge := model.KafkaEdge{Protocol: model.ProtoKafka, Detection: model.DetectKafka, Schema: sch}
	add := func(e model.KafkaEdge) {
		if producer {
			mc.Out.KafkaProducers = append(mc.Out.KafkaProducers, e)
		} else {
			mc.Out.KafkaConsumers = append(mc.Out.KafkaConsumers, e)
		}
	}

	switch vs.Kind {
	case resolve.Exact:
		if len(vs.Values) == 1 {
			edge.Topic, edge.Resolved, edge.Confidence, edge.ResolvedVia = vs.Values[0].S, true, vs.Values[0].Conf, vs.Values[0].Source
			add(edge)
			return
		}
		for _, v := range vs.Values {
			e := edge
			e.Topic, e.Resolved, e.Confidence, e.ResolvedVia = v.S, true, model.Likely, v.Source
			e.Conditional, e.CandidateGroup = true, group
			add(e)
		}
	case resolve.Template:
		edge.Topic, edge.Resolved, edge.Confidence = templateString(vs.Segments), false, model.Uncertain
		add(edge)
	default:
		edge.Resolved, edge.Confidence = false, model.Uncertain
		add(edge)
	}
}

// receiverName is the simple name of a call's receiver, unwrapping this.field.
func receiverName(call java.Node) string {
	obj := call.ChildByFieldName("object")
	if obj.Type() == "field_access" && obj.ChildByFieldName("object").Text() == "this" {
		return obj.ChildByFieldName("field").Text()
	}
	return obj.Text()
}

// kafkaTemplateValueType reports whether `name` is declared as a KafkaTemplate in
// the enclosing method (parameter) or class (field) — a lightweight receiver-type
// check so a generic send() on some other object isn't mistaken for a producer —
// and returns the message value type V from KafkaTemplate<K, V>.
func kafkaTemplateValueType(call java.Node, name string) (valueType string, ok bool) {
	if name == "" {
		return "", false
	}
	if m := enclosingOfTypes(call, "method_declaration", "constructor_declaration"); m.Valid() {
		if params := childByType(m, "formal_parameters"); params.Valid() {
			for _, p := range namedChildren(params) {
				if p.Type() == "formal_parameter" && p.ChildByFieldName("name").Text() == name {
					if v, ok := kafkaValueOf(p.ChildByFieldName("type")); ok {
						return v, true
					}
				}
			}
		}
	}
	if cls := enclosingOfTypes(call, "class_declaration"); cls.Valid() {
		body := cls.ChildByFieldName("body")
		for _, fd := range namedChildren(body) {
			if fd.Type() != "field_declaration" {
				continue
			}
			v, isKT := kafkaValueOf(fd.ChildByFieldName("type"))
			if !isKT {
				continue
			}
			for _, d := range namedChildren(fd) {
				if d.Type() == "variable_declarator" && d.ChildByFieldName("name").Text() == name {
					return v, true
				}
			}
		}
	}
	return "", false
}

// kafkaValueOf reports whether a type is a KafkaTemplate and returns its value
// type parameter V (second of <K, V>).
func kafkaValueOf(t java.Node) (valueType string, ok bool) {
	txt := t.Text()
	if !strings.HasPrefix(txt, "KafkaTemplate") && !strings.HasPrefix(txt, "ReplyingKafkaTemplate") {
		return "", false
	}
	return secondTypeArg(txt), true
}

// secondTypeArg returns the second top-level generic argument of a type string.
func secondTypeArg(typeText string) string {
	i := strings.IndexByte(typeText, '<')
	j := strings.LastIndex(typeText, ">")
	if i < 0 || j <= i {
		return ""
	}
	args, depth, start := []string{}, 0, i+1
	inner := typeText[:j]
	for k := i + 1; k <= len(inner); k++ {
		if k == len(inner) || (inner[k] == ',' && depth == 0) {
			args = append(args, strings.TrimSpace(inner[start:k]))
			start = k + 1
		} else if inner[k] == '<' {
			depth++
		} else if inner[k] == '>' {
			depth--
		}
	}
	if len(args) >= 2 {
		return args[1]
	}
	return ""
}

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

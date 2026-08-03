package springkt

import (
	"fmt"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/kotlin"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// kafkaDetector extracts Kafka edges from Kotlin Spring services, mirroring the
// Java spring.kafkaDetector's core: consumers from @KafkaListener functions,
// producers from KafkaTemplate.send call sites. The producer/consumer -> topic
// edge is emitted whenever the call/annotation is real, independent of whether
// the topic or payload schema resolves — an unresolved topic yields an uncertain
// edge, never a dropped one.
//
// Kotlin has no value-flow evaluator yet, so topic resolution is literal-first:
// a string literal is taken as-is (a `${...}` placeholder resolves through the
// config layer when present), and a non-literal topic (constant/expression) is
// an honest uncertain edge. The advanced Java machinery (Streams, Message-header
// topics, NewTopic beans, outbox) is deferred to follow-ups.
type kafkaDetector struct{}

func (kafkaDetector) Name() string             { return "springkt.kafka" }
func (kafkaDetector) Protocol() model.Protocol { return model.ProtoKafka }

const (
	// any annotated function; the handler filters to @KafkaListener.
	kafkaConsumerQuery = `(function_declaration (modifiers (annotation)) ) @fn`
	// any `<recv>.<name>(<args>)` method call; the handler filters to send().
	kafkaProducerQuery = `(call_expression
  (navigation_expression
    (navigation_suffix (simple_identifier) @name))
  (call_suffix (value_arguments) @args)
) @call`
)

func (d kafkaDetector) Rules() []provider.Rule {
	return []provider.Rule{
		{Query: kafkaConsumerQuery, OnMatch: d.onConsumer},
		{Query: kafkaProducerQuery, OnMatch: d.onProducer},
	}
}

// onConsumer emits a consumed-topic edge per topic on a @KafkaListener function,
// with the message payload's schema attached.
func (kafkaDetector) onConsumer(mc *provider.MatchContext) {
	fn, ok := mc.Captures["fn"].(kotlin.Node)
	if !ok || !fn.Valid() {
		return
	}
	ann := kotlin.FindAnnotation(kotlin.Modifiers(fn), "KafkaListener")
	if !ann.Valid() {
		return
	}
	topics, literal, ok := kotlin.AnnotationStringValues(ann, "topics")
	if !ok {
		return
	}
	sch := kafkaSchema(mc, consumerPayloadType(fn))
	for i, topic := range topics {
		group := fmt.Sprintf("%s:%d:kafka-c%d", mc.File.Path(), ann.StartByte(), i)
		emitKafka(mc, topic, literal, false, group, sch)
	}
}

// onProducer emits a produced-topic edge for a KafkaTemplate.send(topic, ...)
// call, with the template's value-type payload schema attached.
func (kafkaDetector) onProducer(mc *provider.MatchContext) {
	name, _ := mc.Captures["name"].(kotlin.Node)
	call, _ := mc.Captures["call"].(kotlin.Node)
	args, _ := mc.Captures["args"].(kotlin.Node)
	if !name.Valid() || name.Text() != "send" || !args.Valid() {
		return
	}
	valueType, ok := kafkaTemplateValueType(call, producerReceiver(call))
	if !ok {
		return // receiver is not a KafkaTemplate — not a producer
	}
	topicArgs := valueArgs(args)
	if len(topicArgs) == 0 {
		return
	}
	raw, literal := topicArgValue(topicArgs[0])
	group := fmt.Sprintf("%s:%d:kafka-p", mc.File.Path(), topicArgs[0].StartByte())
	emitKafka(mc, raw, literal, true, group, kafkaSchema(mc, valueType))
}

// kafkaSchema resolves a payload type's schema files-first (safe-fail: nil keeps
// the edge, drops only the schema).
func kafkaSchema(mc *provider.MatchContext, payloadType string) *model.Schema {
	if payloadType == "" {
		return nil
	}
	return schema.ResolveKafka(payloadType, mc.Index.Schemas, mc.Index.Types)
}

// consumerPayloadType is the @KafkaListener function's payload parameter type:
// the first parameter that is not a @Header, or "".
func consumerPayloadType(fn kotlin.Node) string {
	for _, p := range kotlin.Params(fn) {
		if p.Mods.Valid() && kotlin.FindAnnotation(p.Mods, "Header").Valid() {
			continue
		}
		typ, _ := kotlin.DeclaredType(p.Node)
		return typ
	}
	return ""
}

// producerReceiver returns the simple name of a send() call's receiver, handling
// `kafkaTemplate.send(...)` (identifier) and `this.kafkaTemplate.send(...)`
// (a nested navigation whose trailing name is the field).
func producerReceiver(call kotlin.Node) string {
	nav := kotlin.ChildByType(call, "navigation_expression")
	if !nav.Valid() || nav.NamedChildCount() == 0 {
		return ""
	}
	first := nav.NamedChild(0)
	switch first.Type() {
	case "simple_identifier":
		return first.Text()
	case "navigation_expression": // this.field / that.field
		if suf := kotlin.ChildByType(first, "navigation_suffix"); suf.Valid() {
			if id := kotlin.ChildByType(suf, "simple_identifier"); id.Valid() {
				return id.Text()
			}
		}
	}
	return ""
}

// kafkaTemplateValueType finds `name`'s declaration in the enclosing class (a
// primary-constructor parameter or a class-body property) and, if it is a
// KafkaTemplate<K, V>, returns the message value type V — the receiver-type
// guard that keeps a generic send() on some other object from being a producer.
func kafkaTemplateValueType(ctx kotlin.Node, name string) (valueType string, ok bool) {
	if name == "" {
		return "", false
	}
	cls := enclosingClass(ctx)
	if !cls.Valid() {
		return "", false
	}
	if pc := kotlin.ChildByType(cls, "primary_constructor"); pc.Valid() {
		for _, p := range kotlin.NamedChildren(pc) {
			if p.Type() != "class_parameter" {
				continue
			}
			if id := kotlin.ChildByType(p, "simple_identifier"); id.Valid() && id.Text() == name {
				if v, isKT := kafkaValueArg(declaredTypeText(p)); isKT {
					return v, true
				}
			}
		}
	}
	if body := kotlin.ChildByType(cls, "class_body"); body.Valid() {
		for _, m := range kotlin.NamedChildren(body) {
			if m.Type() != "property_declaration" {
				continue
			}
			vd := kotlin.ChildByType(m, "variable_declaration")
			if id := kotlin.ChildByType(vd, "simple_identifier"); id.Valid() && id.Text() == name {
				if v, isKT := kafkaValueArg(declaredTypeText(vd)); isKT {
					return v, true
				}
			}
		}
	}
	return "", false
}

// declaredTypeText returns the declared type text (with generics) of a
// class_parameter or variable_declaration, e.g. "KafkaTemplate<String, Order>".
func declaredTypeText(node kotlin.Node) string {
	typ, _ := kotlin.DeclaredType(node)
	return typ
}

// kafkaValueArg reports whether a type is a KafkaTemplate and returns its value
// type parameter V (second of <K, V>).
func kafkaValueArg(typeText string) (valueType string, ok bool) {
	if !strings.HasPrefix(typeText, "KafkaTemplate") && !strings.HasPrefix(typeText, "ReplyingKafkaTemplate") {
		return "", false
	}
	return secondTypeArg(typeText), true
}

// secondTypeArg returns the second top-level generic argument of a type string
// ("KafkaTemplate<String, Order>" -> "Order"), respecting nested generics.
func secondTypeArg(typeText string) string {
	i := strings.IndexByte(typeText, '<')
	j := strings.LastIndexByte(typeText, '>')
	if i < 0 || j <= i {
		return ""
	}
	inner := typeText[i+1 : j]
	var args []string
	depth, start := 0, 0
	for k := 0; k <= len(inner); k++ {
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

// enclosingClass walks up to the class/object declaration that contains n.
func enclosingClass(n kotlin.Node) kotlin.Node {
	for p := n.Parent(); p.Valid(); p = p.Parent() {
		if p.Type() == "class_declaration" || p.Type() == "object_declaration" {
			return p
		}
	}
	return kotlin.Node{}
}

// valueArgs returns the value_argument nodes of a value_arguments list.
func valueArgs(args kotlin.Node) []kotlin.Node {
	var out []kotlin.Node
	for _, c := range kotlin.NamedChildren(args) {
		if c.Type() == "value_argument" {
			out = append(out, c)
		}
	}
	return out
}

// topicArgValue reads a send() topic argument: a string literal's content
// (literal=true) or any other expression's raw text (literal=false — no Kotlin
// evaluator yet).
func topicArgValue(va kotlin.Node) (raw string, literal bool) {
	if sl := kotlin.ChildByType(va, "string_literal"); sl.Valid() {
		if sc := kotlin.ChildByType(sl, "string_content"); sc.Valid() {
			return sc.Text(), true
		}
		return strings.Trim(sl.Text(), `"`), true
	}
	kids := kotlin.NamedChildren(va)
	if len(kids) > 0 {
		return kids[len(kids)-1].Text(), false
	}
	return "", false
}

// emitKafka appends one topic edge (producer or consumer). A plain literal is
// confirmed; a `${...}` placeholder resolves through the config layer (one edge
// per divergent candidate, capped likely); an unresolved placeholder or a
// non-literal topic is an honest uncertain edge.
func emitKafka(mc *provider.MatchContext, raw string, literal, producer bool, group string, sch *model.Schema) {
	add := func(e model.KafkaEdge) {
		if producer {
			mc.Out.KafkaProducers = append(mc.Out.KafkaProducers, e)
		} else {
			mc.Out.KafkaConsumers = append(mc.Out.KafkaConsumers, e)
		}
	}
	base := model.KafkaEdge{Protocol: model.ProtoKafka, Detection: model.DetectKafka, Schema: sch}

	switch {
	case !literal:
		base.Topic, base.Resolved, base.Confidence = raw, false, model.Uncertain
		add(base)
	case strings.Contains(raw, "${"):
		cands := configCandidates(mc, raw)
		switch len(cands) {
		case 0:
			base.Topic, base.Resolved, base.Confidence = raw, false, model.Uncertain
			add(base)
		case 1:
			base.Topic, base.Resolved, base.Confidence, base.ResolvedVia = cands[0].Value, true, cands[0].Conf, cands[0].Source
			add(base)
		default:
			for _, c := range cands {
				e := base
				e.Topic, e.Resolved, e.Confidence, e.ResolvedVia = c.Value, true, model.Likely, c.Source
				e.Conditional, e.CandidateGroup = true, group
				add(e)
			}
		}
	default:
		base.Topic, base.Resolved, base.Confidence = raw, true, model.Confirmed
		add(base)
	}
}

// configCandidates resolves a `${...}` topic placeholder through the config
// layer, or returns nil when no config layer is wired (springkt has none yet).
func configCandidates(mc *provider.MatchContext, placeholder string) []provider.ResolvedValue {
	if mc.Index == nil || mc.Index.Config == nil {
		return nil
	}
	return mc.Index.Config.Candidates(placeholder)
}

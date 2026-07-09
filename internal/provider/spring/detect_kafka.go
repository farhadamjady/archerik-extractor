package spring

import (
	"fmt"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/resolve"
)

// kafkaDetector extracts Kafka edges: producers from KafkaTemplate.send call
// sites, consumers from @KafkaListener methods. The producer/consumer -> topic
// edge is ALWAYS emitted when the call is real, independent of whether the topic
// (or a payload schema, PR 22) resolves — an unresolved topic yields an
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
		{Query: kafkaProducerQuery, OnMatch: d.onProducer},
		{Query: kafkaConsumerQuery, OnMatch: d.onConsumer},
	}
}

// onProducer emits a produced-topic edge for KafkaTemplate.send(topic, ...); the
// topic is the first argument, resolved via the value evaluator.
func (kafkaDetector) onProducer(mc *provider.MatchContext) {
	name, _ := mc.Captures["name"].(java.Node)
	if name.Text() != "send" {
		return
	}
	call, _ := mc.Captures["call"].(java.Node)
	if !kafkaTemplateReceiver(call, receiverName(call)) {
		return
	}
	args, _ := mc.Captures["args"].(java.Node)
	topic := args.NamedChild(0)
	if !topic.Valid() {
		return
	}
	group := fmt.Sprintf("%s:%d:kafka-p", mc.File.Path(), topic.StartByte())
	emitKafkaTopic(mc, resolveNode(mc, topic), true, group)
}

// onConsumer emits a consumed-topic edge per topic on a @KafkaListener.
func (kafkaDetector) onConsumer(mc *provider.MatchContext) {
	ann, _ := mc.Captures["ann"].(java.Node)
	for _, node := range annotationValueNodes(ann, "topics") {
		group := fmt.Sprintf("%s:%d:kafka-c", mc.File.Path(), node.StartByte())
		emitKafkaTopic(mc, resolveTopicNode(mc, node), false, group)
	}
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
		vals[i] = resolve.Value{S: c.Value, Conf: c.Conf}
	}
	return resolve.ExactValues(vals...)
}

// emitKafkaTopic appends a topic edge to the producer or consumer slice. Like
// emitValueSet but for KafkaEdge; the edge is emitted for every kind, including
// Unknown (topic unresolved but the producer/consumer is real).
func emitKafkaTopic(mc *provider.MatchContext, vs resolve.ValueSet, producer bool, group string) {
	edge := model.KafkaEdge{Protocol: model.ProtoKafka, Detection: model.DetectKafka}
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
			edge.Topic, edge.Resolved, edge.Confidence = vs.Values[0].S, true, vs.Values[0].Conf
			add(edge)
			return
		}
		for _, v := range vs.Values {
			e := edge
			e.Topic, e.Resolved, e.Confidence = v.S, true, model.Likely
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

// kafkaTemplateReceiver reports whether `name` is declared as a KafkaTemplate in
// the enclosing method (parameter) or class (field) — a lightweight receiver-type
// check so a generic send() on some other object isn't mistaken for a producer.
func kafkaTemplateReceiver(call java.Node, name string) bool {
	if name == "" {
		return false
	}
	if m := enclosingOfTypes(call, "method_declaration", "constructor_declaration"); m.Valid() {
		if params := childByType(m, "formal_parameters"); params.Valid() {
			for _, p := range namedChildren(params) {
				if p.Type() == "formal_parameter" && p.ChildByFieldName("name").Text() == name &&
					isKafkaTemplateType(p.ChildByFieldName("type")) {
					return true
				}
			}
		}
	}
	if cls := enclosingOfTypes(call, "class_declaration"); cls.Valid() {
		body := cls.ChildByFieldName("body")
		for _, fd := range namedChildren(body) {
			if fd.Type() != "field_declaration" || !isKafkaTemplateType(fd.ChildByFieldName("type")) {
				continue
			}
			for _, d := range namedChildren(fd) {
				if d.Type() == "variable_declarator" && d.ChildByFieldName("name").Text() == name {
					return true
				}
			}
		}
	}
	return false
}

func isKafkaTemplateType(t java.Node) bool {
	txt := t.Text()
	return strings.HasPrefix(txt, "KafkaTemplate") || strings.HasPrefix(txt, "ReplyingKafkaTemplate")
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

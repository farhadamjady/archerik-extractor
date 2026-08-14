package micronaut

import (
	"fmt"
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/java"
	"github.com/farhadamjady/archerik-extractor/internal/resolve"
	"github.com/farhadamjady/archerik-extractor/internal/schema"
)

// kafkaDetector extracts Kafka edges from Micronaut's annotation model. Both
// directions hang off a method-level @Topic; direction is decided by the
// enclosing type's marker: @KafkaClient -> producer, @KafkaListener -> consumer.
// The topic edge is always emitted when the marker is present, even if the topic
// itself is a dynamic parameter (@Topic on a method PARAMETER) or a placeholder —
// an unresolved topic yields an uncertain edge, never a dropped one.
type kafkaDetector struct{}

func (kafkaDetector) Name() string             { return "micronaut.kafka" }
func (kafkaDetector) Protocol() model.Protocol { return model.ProtoKafka }

// @Topic captures need two rules because a bare parameter @Topic (dynamic
// destination) parses as a marker_annotation while a method-level @Topic("x")
// parses as an annotation. Both dispatch to the same handler, which resolves the
// role (producer vs consumer, method-level vs dynamic parameter) from context.
const (
	topicAnnQuery = `(annotation
  name: (identifier) @_n
  (#eq? @_n "Topic")
) @topic`
	topicMarkerQuery = `(marker_annotation
  name: (identifier) @_n
  (#eq? @_n "Topic")
) @topic`
)

func (d kafkaDetector) Rules() []provider.Rule {
	return []provider.Rule{
		{Query: topicAnnQuery, OnMatch: d.onTopic},
		{Query: topicMarkerQuery, OnMatch: d.onTopic},
	}
}

func (kafkaDetector) onTopic(mc *provider.MatchContext) {
	ann, ok := mc.Captures["topic"].(java.Node)
	if !ok || !ann.Valid() {
		return
	}
	method := enclosingOfTypes(ann, "method_declaration")
	if !method.Valid() {
		return
	}
	producer, ok := kafkaDirection(ann)
	if !ok {
		return // @Topic outside a @KafkaClient/@KafkaListener type — not ours
	}
	sch := kafkaSchema(mc, payloadType(method))

	// @Topic on a method PARAMETER means the topic is chosen per-call at runtime:
	// a real edge, but an uncertain (anonymous) one.
	if onParameter(ann) {
		group := fmt.Sprintf("%s:%d:mn-kafka", mc.File.Path(), ann.StartByte())
		emitKafkaTopic(mc, resolve.NewUnknown(), producer, group, sch)
		return
	}

	nodes := java.AnnotationValueNodes(ann, "value")
	if len(nodes) == 0 {
		// Bare positional @Topic("x") — AnnotationValueNodes only returns named
		// args; fall back to the positional string.
		if vals, literal, ok := java.AnnotationStringValues(ann, "value"); ok {
			for _, v := range vals {
				group := fmt.Sprintf("%s:%d:mn-kafka:%s", mc.File.Path(), ann.StartByte(), v)
				emitKafkaTopic(mc, topicValueSet(mc, v, literal), producer, group, sch)
			}
			return
		}
		group := fmt.Sprintf("%s:%d:mn-kafka", mc.File.Path(), ann.StartByte())
		emitKafkaTopic(mc, resolve.NewUnknown(), producer, group, sch)
		return
	}
	for _, node := range nodes {
		group := fmt.Sprintf("%s:%d:mn-kafka", mc.File.Path(), node.StartByte())
		emitKafkaTopic(mc, resolveTopicNode(mc, node), producer, group, sch)
	}
}

// kafkaDirection walks out to the enclosing type and reads its Kafka marker:
// @KafkaClient -> producer (true), @KafkaListener -> consumer (false).
func kafkaDirection(ann java.Node) (producer, ok bool) {
	cls := enclosingOfTypes(ann, "class_declaration", "interface_declaration")
	if !cls.Valid() {
		return false, false
	}
	mods := java.ChildByType(cls, "modifiers")
	if !mods.Valid() {
		return false, false
	}
	if java.FindAnnotation(mods, "KafkaClient").Valid() {
		return true, true
	}
	if java.FindAnnotation(mods, "KafkaListener").Valid() {
		return false, true
	}
	return false, false
}

// onParameter reports whether the annotation sits on a method parameter (a
// dynamic destination) rather than on the method declaration itself.
func onParameter(ann java.Node) bool {
	return enclosingOfTypes(ann, "formal_parameter", "method_declaration").Type() == "formal_parameter"
}

// topicValueSet resolves a single positional @Topic string: a ${...} placeholder
// resolves through config (nil in round 1 -> uncertain); a plain literal is
// taken as-is; a non-literal (constant reference) is left uncertain here (the
// AST node path handles constants via the evaluator).
func topicValueSet(mc *provider.MatchContext, s string, literal bool) resolve.ValueSet {
	if !literal {
		return resolve.NewUnknown()
	}
	if strings.Contains(s, "${") {
		if cfg := mc.Index.Config; cfg != nil {
			return resolvedValuesToVS(cfg.Candidates(s))
		}
		return resolve.NewUnknown()
	}
	return resolve.NewExact(model.Confirmed, s)
}

// resolveTopicNode resolves a @Topic value node: a string literal (possibly a
// ${...} placeholder) or a constant reference through the value evaluator.
func resolveTopicNode(mc *provider.MatchContext, node java.Node) resolve.ValueSet {
	if node.Type() == "string_literal" {
		return topicValueSet(mc, java.Unquote(node.Text()), true)
	}
	return resolveNode(mc, node)
}

func resolveNode(mc *provider.MatchContext, n java.Node) resolve.ValueSet {
	if mc.Resolver == nil {
		return resolve.NewUnknown()
	}
	return mc.Resolver.Resolve(n)
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

// payloadType is the message body parameter type of a Kafka method: the first
// parameter that is not a routing/metadata annotation (@KafkaKey, @Topic,
// @Header, @MessageHeader, @KafkaPartition, ...), or "".
func payloadType(method java.Node) string {
	params := java.ChildByType(method, "formal_parameters")
	if !params.Valid() {
		return ""
	}
	for _, p := range java.NamedChildren(params) {
		if p.Type() != "formal_parameter" {
			continue
		}
		if mods := java.ChildByType(p, "modifiers"); mods.Valid() && hasMetadataAnnotation(mods) {
			continue
		}
		return p.ChildByFieldName("type").Text()
	}
	return ""
}

var kafkaMetadataAnnotations = map[string]bool{
	"KafkaKey": true, "Topic": true, "Header": true, "MessageHeader": true,
	"MessageHeaders": true, "KafkaPartition": true, "KafkaPartitionKey": true,
	"KafkaTimestamp": true,
}

func hasMetadataAnnotation(mods java.Node) bool {
	for _, a := range java.AnnotationsOf(mods) {
		if kafkaMetadataAnnotations[java.AnnotationName(a)] {
			return true
		}
	}
	return false
}

// kafkaSchema resolves a payload type's schema files-first (safe-fail: nil keeps
// the edge, drops only the schema).
func kafkaSchema(mc *provider.MatchContext, pType string) *model.Schema {
	if pType == "" {
		return nil
	}
	return schema.ResolveKafka(pType, mc.Index.Schemas, mc.Index.Types)
}

// emitKafkaTopic appends a topic edge to the producer or consumer slice for
// every ValueSet kind, including Unknown (topic unresolved but the endpoint is
// real).
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

// templateString renders a template with {?} for each hole.
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

// enclosingOfTypes returns the nearest ancestor of n whose type is one of types,
// or an invalid Node.
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

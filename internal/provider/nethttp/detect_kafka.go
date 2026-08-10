package nethttp

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/golang"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// kafkaDetector extracts Kafka edges from the three common Go client
// libraries, gated on the file importing one of them so generic types
// (io.Writer, a `Message` struct) are never mistaken for Kafka:
//
//   - segmentio/kafka-go: kafka.Writer{Topic} -> producer;
//     kafka.ReaderConfig{Topic} -> consumer.
//   - IBM/Shopify sarama: sarama.ProducerMessage{Topic, Value} -> producer
//     (payload from the Value's json.Marshal source); ConsumePartition(topic,…)
//     -> consumer.
//   - confluent-kafka-go: SubscribeTopics([]string{…}) -> consumer; a producer's
//     kafka.TopicPartition{Topic} -> producer.
//
// A literal topic is confirmed; a computed one (a variable/pointer, common in the
// confluent producer form) is an honest uncertain edge.
type kafkaDetector struct{}

func (kafkaDetector) Name() string             { return "nethttp.kafka" }
func (kafkaDetector) Protocol() model.Protocol { return model.ProtoKafka }

const (
	kafkaCompositeQuery = `(composite_literal) @lit`
	kafkaCallQuery      = `(call_expression
  function: (selector_expression field: (field_identifier) @field)
  arguments: (argument_list) @args) @call`
)

func (d kafkaDetector) Rules() []provider.Rule {
	return []provider.Rule{
		{Query: kafkaCompositeQuery, OnMatch: d.onComposite},
		{Query: kafkaCallQuery, OnMatch: d.onCall},
	}
}

// compositeKind maps a Kafka struct's simple type name to its edge direction and
// whether it carries a Value payload (for schema resolution).
var compositeKind = map[string]struct {
	producer   bool
	hasPayload bool
}{
	"Writer":          {true, false},  // segmentio producer config
	"ReaderConfig":    {false, false}, // segmentio consumer config
	"ProducerMessage": {true, true},   // sarama producer message
	"TopicPartition":  {true, false},  // confluent producer target
}

func (kafkaDetector) onComposite(mc *provider.MatchContext) {
	lit, _ := mc.Captures["lit"].(golang.Node)
	if !lit.Valid() || !isKafkaFile(mc.File) {
		return
	}
	kind, ok := compositeKind[compositeTypeName(lit)]
	if !ok {
		return
	}
	topicNode := compositeField(lit, "Topic")
	if !topicNode.Valid() {
		return
	}
	raw, literal := topicValue(topicNode)
	var sch *model.Schema
	if kind.hasPayload {
		sch = messagePayloadSchema(mc, lit)
	}
	emitKafkaEdge(mc, raw, literal, kind.producer, sch)
}

func (kafkaDetector) onCall(mc *provider.MatchContext) {
	field, _ := mc.Captures["field"].(golang.Node)
	args, _ := mc.Captures["args"].(golang.Node)
	if !field.Valid() || !args.Valid() || !isKafkaFile(mc.File) {
		return
	}
	switch field.Text() {
	case "ConsumePartition": // sarama: ConsumePartition(topic, partition, offset)
		if a := golang.NamedChildren(args); len(a) >= 1 {
			raw, literal := topicValue(a[0])
			emitKafkaEdge(mc, raw, literal, false, nil)
		}
	case "SubscribeTopics": // confluent: SubscribeTopics([]string{"a","b"}, …)
		if a := golang.NamedChildren(args); len(a) >= 1 {
			for _, tv := range sliceStringTopics(a[0]) {
				emitKafkaEdge(mc, tv.raw, tv.literal, false, nil)
			}
		}
	}
}

// emitKafkaEdge appends one topic edge (producer or consumer).
func emitKafkaEdge(mc *provider.MatchContext, raw string, literal, producer bool, sch *model.Schema) {
	if raw == "" {
		return
	}
	edge := model.KafkaEdge{Protocol: model.ProtoKafka, Detection: model.DetectKafka, Schema: sch}
	if literal {
		edge.Topic, edge.Resolved, edge.Confidence = raw, true, model.Confirmed
	} else {
		edge.Topic, edge.Resolved, edge.Confidence = raw, false, model.Uncertain
	}
	if producer {
		mc.Out.KafkaProducers = append(mc.Out.KafkaProducers, edge)
	} else {
		mc.Out.KafkaConsumers = append(mc.Out.KafkaConsumers, edge)
	}
}

// isKafkaFile reports whether the file imports a recognized Go Kafka client.
func isKafkaFile(f provider.ParsedFile) bool {
	jf, ok := f.(*golang.File)
	if !ok {
		return false
	}
	src := string(jf.Src())
	return strings.Contains(src, "segmentio/kafka-go") ||
		strings.Contains(src, "Shopify/sarama") ||
		strings.Contains(src, "IBM/sarama") ||
		strings.Contains(src, "confluent-kafka-go")
}

// compositeTypeName returns a composite literal's simple type name
// (kafka.Writer -> "Writer", ProducerMessage -> "ProducerMessage").
func compositeTypeName(lit golang.Node) string {
	ty := lit.ChildByFieldName("type")
	switch ty.Type() {
	case "qualified_type":
		if n := ty.ChildByFieldName("name"); n.Valid() {
			return n.Text()
		}
		if n := golang.ChildByType(ty, "type_identifier"); n.Valid() {
			return n.Text()
		}
	case "type_identifier":
		return ty.Text()
	}
	return ""
}

// compositeField returns the value expression of a `Field: value` element in a
// composite literal, or an invalid node when absent.
func compositeField(lit golang.Node, field string) golang.Node {
	lv := golang.ChildByType(lit, "literal_value")
	if !lv.Valid() {
		return golang.Node{}
	}
	for _, ke := range golang.NamedChildren(lv) {
		if ke.Type() != "keyed_element" {
			continue
		}
		els := golang.NamedChildren(ke)
		if len(els) == 2 && els[0].Text() == field {
			return els[1].NamedChild(0)
		}
	}
	return golang.Node{}
}

// topicValue reads a topic expression: a string literal (confirmed) or any other
// expression's text (uncertain).
func topicValue(n golang.Node) (raw string, literal bool) {
	if v, ok := golang.StringLit(n); ok {
		return v, true
	}
	return n.Text(), false
}

type topicLit struct {
	raw     string
	literal bool
}

// sliceStringTopics reads the string elements of a `[]string{"a","b"}` literal.
func sliceStringTopics(n golang.Node) []topicLit {
	if n.Type() != "composite_literal" {
		return nil
	}
	lv := golang.ChildByType(n, "literal_value")
	if !lv.Valid() {
		return nil
	}
	var out []topicLit
	for _, e := range golang.NamedChildren(lv) {
		if e.Type() == "literal_element" {
			e = e.NamedChild(0) // unwrap the element to its string value
		}
		if v, ok := golang.StringLit(e); ok {
			out = append(out, topicLit{v, true})
		}
	}
	return out
}

// messagePayloadSchema resolves the schema of a message's Value field: the struct
// serialized into it via json.Marshal (possibly through an encoder wrapper and one
// local binding). Returns nil when unresolvable (edge kept, schema dropped).
func messagePayloadSchema(mc *provider.MatchContext, lit golang.Node) *model.Schema {
	val := compositeField(lit, "Value")
	if !val.Valid() {
		return nil
	}
	body := enclosingFuncBody(lit)
	t := marshaledStructType(val, body, 0)
	if t == "" {
		return nil
	}
	return schema.ResolveKafka(normalizeGoType(t), mc.Index.Schemas, mc.Index.Types)
}

// marshaledStructType finds the struct type serialized into a value expression:
// a json.Marshal/Encode(x) call within it (x resolved to a composite type), or a
// local var that itself holds such marshaled bytes. Bounded recursion follows
// one-hop `b := json.Marshal(x)` bindings.
func marshaledStructType(node, funcBody golang.Node, depth int) string {
	if depth > 4 || !node.Valid() {
		return ""
	}
	if t := scanMarshalArg(node, funcBody, depth); t != "" {
		return t
	}
	if node.Type() == "identifier" && funcBody.Valid() {
		if rhs := shortVarRHS(funcBody, node.Text()); rhs.Valid() {
			return scanMarshalArg(rhs, funcBody, depth+1)
		}
	}
	return ""
}

// scanMarshalArg walks node for a JSON encode call (json.Marshal / an encoder
// like sarama.ByteEncoder) and resolves its argument to a struct type.
func scanMarshalArg(node, funcBody golang.Node, depth int) string {
	found := ""
	node.Walk(func(n golang.Node) bool {
		if found != "" || n.Type() != "call_expression" {
			return found == ""
		}
		if !isEncodeCall(callName(n)) {
			return true
		}
		for _, a := range callArgs(n) {
			if t := exprStructType(a, funcBody, depth); t != "" {
				found = t
				return false
			}
		}
		return true
	})
	return found
}

// exprStructType resolves an expression to a struct type: a composite literal
// `T{...}` (or `&T{...}`), or an identifier bound to one — or, one hop further, an
// identifier holding json.Marshal(x)'s bytes.
func exprStructType(arg, funcBody golang.Node, depth int) string {
	if arg.Type() == "unary_expression" {
		if op := arg.ChildByFieldName("operand"); op.Valid() {
			arg = op
		}
	}
	if t := compositeType(arg); t != "" {
		return t
	}
	if arg.Type() == "identifier" && funcBody.Valid() {
		if t := localCompositeType(funcBody, arg.Text()); t != "" {
			return t
		}
		return marshaledStructType(arg, funcBody, depth) // b that holds marshal(x)
	}
	return ""
}

// localCompositeType finds `name := T{...}` in funcBody and returns T.
func localCompositeType(funcBody golang.Node, name string) string {
	t := ""
	funcBody.Walk(func(n golang.Node) bool {
		if t != "" || n.Type() != "short_var_declaration" {
			return t == ""
		}
		ls := golang.NamedChildren(n.ChildByFieldName("left"))
		rs := golang.NamedChildren(n.ChildByFieldName("right"))
		if len(ls) == 1 && len(rs) == 1 && ls[0].Type() == "identifier" && ls[0].Text() == name {
			t = compositeType(rs[0])
		}
		return true
	})
	return t
}

// shortVarRHS finds the single right-hand expression of a `name, … := <rhs>`
// declaration (e.g. b in `b, _ := json.Marshal(evt)`).
func shortVarRHS(funcBody golang.Node, name string) golang.Node {
	var rhs golang.Node
	funcBody.Walk(func(n golang.Node) bool {
		if rhs.Valid() || n.Type() != "short_var_declaration" {
			return !rhs.Valid()
		}
		rs := golang.NamedChildren(n.ChildByFieldName("right"))
		if len(rs) != 1 {
			return true
		}
		for _, l := range golang.NamedChildren(n.ChildByFieldName("left")) {
			if l.Type() == "identifier" && l.Text() == name {
				rhs = rs[0]
				return false
			}
		}
		return true
	})
	return rhs
}

// enclosingFuncBody returns the block body of the function/method containing n.
func enclosingFuncBody(n golang.Node) golang.Node {
	for p := n.Parent(); p.Valid(); p = p.Parent() {
		if p.Type() == "function_declaration" || p.Type() == "method_declaration" {
			return p.ChildByFieldName("body")
		}
	}
	return golang.Node{}
}

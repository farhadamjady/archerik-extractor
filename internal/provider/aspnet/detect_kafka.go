package aspnet

import (
	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/csharp"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// kafkaDetector extracts Kafka edges from Confluent.Kafka call sites:
//
//   - IProducer<K, V>.Produce / ProduceAsync(topic, …) -> producer
//   - IConsumer<K, V>.Subscribe(topic)                 -> consumer
//
// The receiver-type guard resolves the receiver (a field or constructor
// parameter of the enclosing class) to an IProducer<K, V> / IConsumer<K, V> and
// reads the message value type V from the generic; a Produce/Subscribe on
// anything else is ignored. The payload schema comes from V via the C# type
// index (schema.ResolveKafka). A literal topic is confirmed; a computed one is an
// honest uncertain edge.
type kafkaDetector struct{}

func (kafkaDetector) Name() string             { return "aspnet.kafka" }
func (kafkaDetector) Protocol() model.Protocol { return model.ProtoKafka }

// kafkaCallQuery captures every `<expr>.<name>(args)` invocation; the handler
// filters to Produce/ProduceAsync/Subscribe on a Confluent client.
const kafkaCallQuery = `(invocation_expression
  function: (member_access_expression
    name: (_) @method)
  arguments: (argument_list) @args) @call`

func (d kafkaDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: kafkaCallQuery, OnMatch: d.onCall}}
}

func (kafkaDetector) onCall(mc *provider.MatchContext) {
	method, _ := mc.Captures["method"].(csharp.Node)
	args, _ := mc.Captures["args"].(csharp.Node)
	call, _ := mc.Captures["call"].(csharp.Node)
	if !method.Valid() || !args.Valid() || !call.Valid() {
		return
	}
	recv := kafkaReceiverName(call)
	if recv == "" {
		return
	}
	switch methodName(method) {
	case "Produce", "ProduceAsync":
		if v, ok := kafkaValueType(call, recv, "IProducer"); ok {
			emitKafka(mc, args, true, v)
		}
	case "Subscribe":
		if v, ok := kafkaValueType(call, recv, "IConsumer"); ok {
			emitKafka(mc, args, false, v)
		}
	}
}

// emitKafka appends one topic edge with the payload schema resolved from the
// client's value type V. The topic is the call's first argument.
func emitKafka(mc *provider.MatchContext, args csharp.Node, producer bool, valueType string) {
	raw, literal, ok := topicArg(args)
	if !ok {
		return
	}
	edge := model.KafkaEdge{
		Protocol:  model.ProtoKafka,
		Detection: model.DetectKafka,
		Schema:    schema.ResolveKafka(valueType, mc.Index.Schemas, mc.Index.Types),
	}
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

// topicArg reads the first argument as a topic: a string literal (confirmed) or
// any other expression (uncertain). ok=false when there is no argument.
func topicArg(args csharp.Node) (raw string, literal, ok bool) {
	arg := firstArgExpr(args)
	if !arg.Valid() {
		return "", false, false
	}
	if arg.Type() == "string_literal" {
		return csharp.StringContent(arg), true, true
	}
	return arg.Text(), false, true
}

// kafkaReceiverName is the simple name of an invocation's receiver: `_producer`
// (identifier) or `this._producer` (the trailing member name).
func kafkaReceiverName(call csharp.Node) string {
	fn := call.ChildByFieldName("function")
	if fn.Type() != "member_access_expression" {
		return ""
	}
	recv := fn.ChildByFieldName("expression")
	switch recv.Type() {
	case "identifier":
		return recv.Text()
	case "member_access_expression": // this._producer / that._producer
		if nm := recv.ChildByFieldName("name"); nm.Valid() {
			return nm.Text()
		}
	}
	return ""
}

// kafkaValueType finds `name`'s declaration in the enclosing class — a field, a
// property, a constructor parameter, or a primary-constructor parameter — and, if
// it is `wantIface`<K, V> (IProducer / IConsumer), returns the value type V.
func kafkaValueType(ctx csharp.Node, name, wantIface string) (string, bool) {
	cls := enclosingType(ctx)
	if !cls.Valid() {
		return "", false
	}
	// primary-constructor parameters live on the type declaration itself.
	if v, ok := paramListValueType(csharp.ChildByType(cls, "parameter_list"), name, wantIface); ok {
		return v, true
	}
	body := csharp.ChildByType(cls, "declaration_list")
	if !body.Valid() {
		return "", false
	}
	for _, m := range csharp.NamedChildren(body) {
		switch m.Type() {
		case "field_declaration", "property_declaration":
			vd := csharp.ChildByType(m, "variable_declaration")
			if fieldDeclaresName(vd, name) {
				if v, ok := genericValueArg(csharp.ChildByType(vd, "generic_name"), wantIface); ok {
					return v, true
				}
			}
		case "constructor_declaration":
			if v, ok := paramListValueType(csharp.ChildByType(m, "parameter_list"), name, wantIface); ok {
				return v, true
			}
		}
	}
	return "", false
}

// enclosingType walks up to the class/record/struct that contains n.
func enclosingType(n csharp.Node) csharp.Node {
	for p := n.Parent(); p.Valid(); p = p.Parent() {
		switch p.Type() {
		case "class_declaration", "record_declaration", "struct_declaration":
			return p
		}
	}
	return csharp.Node{}
}

// fieldDeclaresName reports whether a variable_declaration declares a variable
// named `name`.
func fieldDeclaresName(vd csharp.Node, name string) bool {
	if !vd.Valid() {
		return false
	}
	for _, d := range csharp.NamedChildren(vd) {
		if d.Type() == "variable_declarator" {
			if id := csharp.ChildByType(d, "identifier"); id.Valid() && id.Text() == name {
				return true
			}
		}
	}
	return false
}

// paramListValueType finds a parameter named `name` in a parameter_list and, if
// its type is wantIface<K, V>, returns V.
func paramListValueType(pl csharp.Node, name, wantIface string) (string, bool) {
	if !pl.Valid() {
		return "", false
	}
	for _, p := range csharp.NamedChildren(pl) {
		if p.Type() != "parameter" {
			continue
		}
		if nm := p.ChildByFieldName("name"); nm.Valid() && nm.Text() == name {
			return genericValueArg(p.ChildByFieldName("type"), wantIface)
		}
	}
	return "", false
}

// genericValueArg returns the value type V (second type argument) of a
// generic_name whose base identifier is wantIface (IProducer / IConsumer).
func genericValueArg(gn csharp.Node, wantIface string) (string, bool) {
	if !gn.Valid() || gn.Type() != "generic_name" {
		return "", false
	}
	base := csharp.ChildByType(gn, "identifier")
	if !base.Valid() || base.Text() != wantIface {
		return "", false
	}
	tal := csharp.ChildByType(gn, "type_argument_list")
	if !tal.Valid() {
		return "", false
	}
	args := csharp.NamedChildren(tal)
	if len(args) >= 2 {
		return args[1].Text(), true
	}
	return "", false
}

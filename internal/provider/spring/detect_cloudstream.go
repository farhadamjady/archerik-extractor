package spring

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// cloudStreamDetector extracts Spring Cloud Stream functional bindings: a
// @Bean method returning Consumer<T> / Supplier<T> /
// Function<T,R> is a message endpoint. Binding names follow the convention
// <method>-in-0 / <method>-out-0; the topic comes from
// spring.cloud.stream.bindings.<binding>.destination, default = binding name.
//
// Gated on the config actually containing spring.cloud.stream keys — a plain
// java.util.function @Bean in a non-stream app emits nothing. Protocol is
// kafka (the dominant binder); a rabbit binder would need a protocol switch,
// noted for later.
type cloudStreamDetector struct{}

func (cloudStreamDetector) Name() string             { return "spring.cloudstream" }
func (cloudStreamDetector) Protocol() model.Protocol { return model.ProtoKafka }

const (
	cloudStreamQuery = `(method_declaration) @method`
	// StreamBridge.send(<binding>, <payload>) — the imperative producer.
	streamBridgeQuery = `(method_invocation
  name: (identifier) @name
  arguments: (argument_list) @args
) @call`
)

func (d cloudStreamDetector) Rules() []provider.Rule {
	return []provider.Rule{
		{Query: cloudStreamQuery, OnMatch: d.onMethod},
		{Query: streamBridgeQuery, OnMatch: d.onStreamBridgeSend},
	}
}

// onStreamBridgeSend handles the imperative producer
// streamBridge.send(binding, payload). The binding (arg0, a string literal)
// maps to its destination(s) via
// spring.cloud.stream.bindings.<binding>.destination; a composite
// (comma-separated) destination fans out to one producer edge each.
func (cloudStreamDetector) onStreamBridgeSend(mc *provider.MatchContext) {
	if !hasCloudStreamConfig(mc.Index) {
		return
	}
	name, _ := mc.Captures["name"].(java.Node)
	call, _ := mc.Captures["call"].(java.Node)
	args, _ := mc.Captures["args"].(java.Node)
	if !name.Valid() || name.Text() != "send" || !args.Valid() {
		return
	}
	if !receiverIsStreamBridge(call) {
		return // a send() on something other than a StreamBridge
	}
	binding, ok := stringLiteralArg(args.NamedChild(0))
	if !ok {
		return // dynamic binding name — could resolve via the evaluator later
	}
	sch := schema.ResolveKafka(streamBridgePayloadType(args), mc.Index.Schemas, mc.Index.Types)
	for _, d := range bindingDestinations(mc, binding) {
		mc.Out.KafkaProducers = append(mc.Out.KafkaProducers, model.KafkaEdge{
			Topic:       d.topic,
			Resolved:    true,
			Schema:      sch,
			Protocol:    model.ProtoKafka,
			Detection:   model.DetectCloudStream,
			Confidence:  d.conf,
			ResolvedVia: d.via,
		})
	}
}

// destination is one resolved output target of a binding.
type destination struct {
	topic string
	conf  model.Confidence
	via   string
}

// bindingDestinations resolves a binding's destination(s):
// spring.cloud.stream.bindings.<binding>.destination, splitting a composite
// (comma-separated) value into one destination each. Absent config defaults to
// the binding name (Spring's dynamic-destination fallback), at likely.
func bindingDestinations(mc *provider.MatchContext, binding string) []destination {
	raw, conf, via := binding, model.Likely, ""
	if cfg := mc.Index.Config; cfg != nil {
		if v, c, src, ok := cfg.Resolve("${spring.cloud.stream.bindings." + binding + ".destination}"); ok && v != "" {
			raw, conf, via = v, c, src
		}
	}
	var out []destination
	for _, part := range strings.Split(raw, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, destination{topic: t, conf: conf, via: via})
		}
	}
	if len(out) == 0 {
		out = append(out, destination{topic: binding, conf: model.Likely})
	}
	return out
}

// receiverIsStreamBridge reports whether the call's receiver is declared as a
// StreamBridge in the enclosing method (parameter) or class (field) — the guard
// that keeps a generic send() on some other object from being a producer.
func receiverIsStreamBridge(call java.Node) bool {
	name := receiverName(call)
	if name == "" {
		return false
	}
	if m := enclosingOfTypes(call, "method_declaration", "constructor_declaration"); m.Valid() {
		if params := childByType(m, "formal_parameters"); params.Valid() {
			for _, p := range namedChildren(params) {
				if p.Type() == "formal_parameter" && p.ChildByFieldName("name").Text() == name {
					return isStreamBridgeType(p.ChildByFieldName("type").Text())
				}
			}
		}
	}
	if cls := enclosingOfTypes(call, "class_declaration"); cls.Valid() {
		body := cls.ChildByFieldName("body")
		for _, fd := range namedChildren(body) {
			if fd.Type() != "field_declaration" || !isStreamBridgeType(fd.ChildByFieldName("type").Text()) {
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

func isStreamBridgeType(t string) bool {
	t = strings.TrimSpace(t)
	return t == "StreamBridge" || strings.HasSuffix(t, ".StreamBridge")
}

// stringLiteralArg returns a string-literal argument's unquoted value.
func stringLiteralArg(n java.Node) (string, bool) {
	if n.Valid() && n.Type() == "string_literal" {
		return unquote(n.Text()), true
	}
	return "", false
}

// streamBridgePayloadType best-effort resolves the message payload type from the
// send()'s second argument: a `new Foo(...)` creation or a local/parameter whose
// declared type is known. Unresolved payloads yield "" (edge kept, schema nil).
func streamBridgePayloadType(args java.Node) string {
	data := args.NamedChild(1)
	if !data.Valid() {
		return ""
	}
	switch data.Type() {
	case "object_creation_expression":
		if t := data.ChildByFieldName("type"); t.Valid() {
			return t.Text()
		}
	case "identifier":
		return localOrParamType(data, data.Text())
	}
	return ""
}

// localOrParamType returns the declared type of a local variable or method
// parameter named `name` in the enclosing method.
func localOrParamType(ctx java.Node, name string) string {
	method := enclosingOfTypes(ctx, "method_declaration", "constructor_declaration")
	if !method.Valid() {
		return ""
	}
	if params := childByType(method, "formal_parameters"); params.Valid() {
		for _, p := range namedChildren(params) {
			if p.Type() == "formal_parameter" && p.ChildByFieldName("name").Text() == name {
				return p.ChildByFieldName("type").Text()
			}
		}
	}
	var typ string
	method.Walk(func(m java.Node) bool {
		if m.Type() != "local_variable_declaration" {
			return true
		}
		for _, d := range namedChildren(m) {
			if d.Type() == "variable_declarator" && d.ChildByFieldName("name").Text() == name {
				typ = m.ChildByFieldName("type").Text()
			}
		}
		return true
	})
	return typ
}

func (cloudStreamDetector) onMethod(mc *provider.MatchContext) {
	if !hasCloudStreamConfig(mc.Index) {
		return
	}
	m, _ := mc.Captures["method"].(java.Node)
	mods := childByType(m, "modifiers")
	if !mods.Valid() || !findAnnotation(mods, "Bean").Valid() {
		return
	}
	kind, in, out := functionalKind(m.ChildByFieldName("type").Text())
	if kind == "" {
		return
	}
	name := m.ChildByFieldName("name").Text()

	defs := functionDefinitions(mc.Index)
	if len(defs) == 0 {
		// No explicit composition: each functional bean binds on its own name.
		if in != "" {
			emitBinding(mc, name+"-in-0", in, false)
		}
		if out != "" {
			emitBinding(mc, name+"-out-0", out, true)
		}
		return
	}
	// Composition declared: only beans named in spring.cloud.function.definition
	// are active, and a composed chain a|b exposes ONE input (from the first
	// bean) and ONE output (from the last) on the composite binding name — the
	// intermediate hand-off is internal, never a topic. So per bean: emit IN only
	// if it is first, OUT only if it is last.
	for _, chain := range defs {
		pos := indexOfStr(chain, name)
		if pos < 0 {
			continue // this bean isn't part of an active definition
		}
		composite := strings.Join(chain, "|")
		if pos == 0 && in != "" {
			emitBinding(mc, composite+"-in-0", in, false)
		}
		if pos == len(chain)-1 && out != "" {
			emitBinding(mc, composite+"-out-0", out, true)
		}
	}
}

// functionDefinitions parses spring.cloud.function.definition into composition
// chains: "a|b;c" -> [["a","b"], ["c"]]. Empty when unset (per-bean binding).
func functionDefinitions(idx *provider.Index) [][]string {
	cfg := idx.Config
	if cfg == nil {
		return nil
	}
	v, _, _, ok := cfg.Resolve("${spring.cloud.function.definition}")
	if !ok || strings.TrimSpace(v) == "" {
		return nil
	}
	var out [][]string
	for _, group := range strings.Split(v, ";") {
		var chain []string
		for _, fn := range strings.Split(group, "|") {
			if f := strings.TrimSpace(fn); f != "" {
				chain = append(chain, f)
			}
		}
		if len(chain) > 0 {
			out = append(out, chain)
		}
	}
	return out
}

func indexOfStr(ss []string, s string) int {
	for i, x := range ss {
		if x == s {
			return i
		}
	}
	return -1
}

// functionalKind classifies the return type: Consumer<T> consumes T;
// Supplier<T> produces T; Function<T,R> consumes T and produces R.
func functionalKind(ret string) (kind, inType, outType string) {
	base := ret
	var args []string
	if i := strings.IndexByte(ret, '<'); i >= 0 && strings.HasSuffix(ret, ">") {
		base = ret[:i]
		args = splitTopLevel(ret[i+1 : len(ret)-1])
	}
	switch simpleName := base[strings.LastIndex(base, ".")+1:]; simpleName {
	case "Consumer":
		if len(args) == 1 {
			return "consumer", args[0], ""
		}
	case "Supplier":
		if len(args) == 1 {
			return "supplier", "", args[0]
		}
	case "Function":
		if len(args) == 2 {
			return "function", args[0], args[1]
		}
	}
	return "", "", ""
}

// emitBinding resolves the binding's destination and emits the topic edge with
// the payload schema (files-first, like the Kafka detector).
func emitBinding(mc *provider.MatchContext, binding, payloadType string, producer bool) {
	topic, conf, via := binding, model.Likely, "" // Cloud Stream default: destination = binding name
	if cfg := mc.Index.Config; cfg != nil {
		if v, c, src, ok := cfg.Resolve("${spring.cloud.stream.bindings." + binding + ".destination}"); ok {
			topic, conf, via = v, c, src
		}
	}
	edge := model.KafkaEdge{
		Topic:       topic,
		Resolved:    true,
		Schema:      schema.ResolveKafka(payloadType, mc.Index.Schemas, mc.Index.Types),
		Protocol:    model.ProtoKafka,
		Detection:   model.DetectCloudStream,
		Confidence:  conf,
		ResolvedVia: via,
	}
	if producer {
		mc.Out.KafkaProducers = append(mc.Out.KafkaProducers, edge)
	} else {
		mc.Out.KafkaConsumers = append(mc.Out.KafkaConsumers, edge)
	}
}

// hasCloudStreamConfig reports whether any spring.cloud.stream key exists in
// the merged config (the gate for this detector).
func hasCloudStreamConfig(idx *provider.Index) bool {
	sc, ok := idx.Config.(*springConfig)
	if !ok {
		return false
	}
	for k := range sc.values {
		if strings.HasPrefix(k, "spring.cloud.stream") {
			return true
		}
	}
	for k := range sc.fallback {
		if strings.HasPrefix(k, "spring.cloud.stream") {
			return true
		}
	}
	return false
}

// splitTopLevel splits generic args on top-level commas (bracket-aware).
func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if strings.TrimSpace(s[start:]) != "" {
		out = append(out, strings.TrimSpace(s[start:]))
	}
	return out
}

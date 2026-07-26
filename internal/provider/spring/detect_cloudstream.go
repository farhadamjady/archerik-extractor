package spring

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// cloudStreamDetector extracts Spring Cloud Stream functional bindings
// (IMPROVEMENTS #14): a @Bean method returning Consumer<T> / Supplier<T> /
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

const cloudStreamQuery = `(method_declaration) @method`

func (d cloudStreamDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: cloudStreamQuery, OnMatch: d.onMethod}}
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
	ret := m.ChildByFieldName("type").Text()
	kind, in, out := functionalKind(ret)
	if kind == "" {
		return
	}
	name := m.ChildByFieldName("name").Text()
	if in != "" {
		emitBinding(mc, name+"-in-0", in, false)
	}
	if out != "" {
		emitBinding(mc, name+"-out-0", out, true)
	}
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

package spring

import (
	"fmt"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/resolve"
)

// reactiveKafkaDetector extracts reactor-kafka / spring-kafka reactive edges
// (IMPROVEMENTS #39), the style used by galleog/piggymetrics-k8s and other
// WebFlux services — neither KafkaTemplate nor @KafkaListener nor Cloud Stream:
//
//   - Producer: producerTemplate.send(SenderRecord.create(
//     new ProducerRecord<>(topic, key, value), meta)). The topic is arg0 of
//     the ProducerRecord constructor (here a @Value-backed field). We key off the
//     ProducerRecord construction itself, which also covers a raw Kafka
//     Producer.send(new ProducerRecord<>(...)).
//   - Consumer: a bean class implementing
//     Function<Flux<ConsumerRecord<K, V>>, Mono<Void>> (or Consumer<Flux<…>>),
//     wired to a ReactiveKafkaConsumerTemplate whose subscription is the config
//     property spring.kafka.consumer.subscribeTopics. The payload is V.
type reactiveKafkaDetector struct{}

func (reactiveKafkaDetector) Name() string             { return "spring.reactivekafka" }
func (reactiveKafkaDetector) Protocol() model.Protocol { return model.ProtoKafka }

const (
	producerRecordQuery   = `(object_creation_expression) @new`
	reactiveConsumerQuery = `(class_declaration) @class`
)

func (d reactiveKafkaDetector) Rules() []provider.Rule {
	return []provider.Rule{
		{Query: producerRecordQuery, OnMatch: d.onProducerRecord},
		{Query: reactiveConsumerQuery, OnMatch: d.onConsumerClass},
	}
}

// onProducerRecord emits a producer edge for `new ProducerRecord<>(topic, …)`.
// The topic (arg0) resolves through the value+config layer; the payload type is
// the value V of the class's ReactiveKafkaProducerTemplate<K, V>, if present.
func (reactiveKafkaDetector) onProducerRecord(mc *provider.MatchContext) {
	n, _ := mc.Captures["new"].(java.Node)
	base, _ := splitGeneric(n.ChildByFieldName("type").Text())
	if simpleTypeName(base) != "ProducerRecord" {
		return
	}
	topic := n.ChildByFieldName("arguments").NamedChild(0)
	if !topic.Valid() {
		return
	}
	group := fmt.Sprintf("%s:%d:rkafka-p", mc.File.Path(), topic.StartByte())
	emitKafkaTopic(mc, resolveNode(mc, topic), true, group, kafkaSchema(mc, reactiveProducerValueType(n)))
}

// onConsumerClass emits consumer edges for a class implementing the reactive
// Kafka consumer functional interface. Topics come from the service's
// spring.kafka.consumer.subscribeTopics; the payload is unwrapped from
// Flux<ConsumerRecord<K, V>>. One independent edge per subscribed topic; no
// topic resolvable -> one honest uncertain edge (the consumer is still real).
func (reactiveKafkaDetector) onConsumerClass(mc *provider.MatchContext) {
	cls, _ := mc.Captures["class"].(java.Node)
	payload, ok := reactiveConsumerPayload(cls)
	if !ok {
		return
	}
	sch := kafkaSchema(mc, payload)
	group := fmt.Sprintf("%s:%d:rkafka-c", mc.File.Path(), cls.StartByte())
	topics := subscribeTopicValues(mc)
	if len(topics) == 0 {
		emitKafkaTopic(mc, resolve.NewUnknown(), false, group, sch)
		return
	}
	for _, v := range topics {
		emitKafkaTopic(mc, resolve.ExactValues(v), false, group, sch)
	}
}

// reactiveConsumerPayload returns the message payload type V if the class
// implements Function<Flux<ConsumerRecord<K, V>>, …> or Consumer<Flux<…>>. It
// unwraps the Flux and the ConsumerRecord/ReceiverRecord/Message wrapper.
func reactiveConsumerPayload(cls java.Node) (string, bool) {
	si := childByType(cls, "super_interfaces")
	if !si.Valid() {
		return "", false
	}
	tl := childByType(si, "type_list")
	if !tl.Valid() {
		return "", false
	}
	for _, ty := range namedChildren(tl) {
		if p, ok := fluxRecordPayload(ty.Text()); ok {
			return p, true
		}
	}
	return "", false
}

// fluxRecordPayload extracts V from Function<Flux<ConsumerRecord<K, V>>, R> or
// Consumer<Flux<ConsumerRecord<K, V>>>. Also accepts Flux<Message<V>> and a bare
// Flux<V>. Returns ok=false for any other shape.
func fluxRecordPayload(iface string) (string, bool) {
	base, args := splitGeneric(iface)
	switch simpleTypeName(base) {
	case "Function", "Consumer":
	default:
		return "", false
	}
	if len(args) == 0 {
		return "", false
	}
	fbase, fargs := splitGeneric(args[0]) // Flux<...>
	if simpleTypeName(fbase) != "Flux" || len(fargs) != 1 {
		return "", false
	}
	rbase, rargs := splitGeneric(fargs[0]) // ConsumerRecord<K,V> / Message<V> / V
	switch simpleTypeName(rbase) {
	case "ConsumerRecord", "ReceiverRecord":
		if len(rargs) == 2 {
			return rargs[1], true
		}
	case "Message":
		if len(rargs) == 1 {
			return rargs[0], true
		}
	default:
		if len(rargs) == 0 { // bare Flux<Event>
			return fargs[0], true
		}
	}
	return "", false
}

// reactiveProducerValueType returns V of the enclosing class's
// ReactiveKafkaProducerTemplate<K, V> field, if any (best-effort schema source).
func reactiveProducerValueType(ctx java.Node) string {
	cls := enclosingOfTypes(ctx, "class_declaration")
	if !cls.Valid() {
		return ""
	}
	body := cls.ChildByFieldName("body")
	for _, fd := range namedChildren(body) {
		if fd.Type() != "field_declaration" {
			continue
		}
		if t := fd.ChildByFieldName("type").Text(); strings.HasPrefix(t, "ReactiveKafkaProducerTemplate") {
			return secondTypeArg(t)
		}
	}
	return ""
}

// subscribeTopicValues resolves spring.kafka.consumer.subscribeTopics through the
// config layer and splits it into one value per comma-separated topic.
func subscribeTopicValues(mc *provider.MatchContext) []resolve.Value {
	cfg := mc.Index.Config
	if cfg == nil {
		return nil
	}
	v, conf, src, ok := cfg.Resolve("${spring.kafka.consumer.subscribeTopics}")
	if !ok || strings.TrimSpace(v) == "" {
		return nil
	}
	var vals []resolve.Value
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			vals = append(vals, resolve.Value{S: p, Conf: conf, Source: src})
		}
	}
	return vals
}

// splitGeneric splits "Outer<A, B>" into ("Outer", ["A","B"]); a non-generic type
// returns (itself, nil).
func splitGeneric(t string) (base string, args []string) {
	t = strings.TrimSpace(t)
	i := strings.IndexByte(t, '<')
	if i < 0 || !strings.HasSuffix(t, ">") {
		return t, nil
	}
	return strings.TrimSpace(t[:i]), splitTopLevel(t[i+1 : len(t)-1])
}

// simpleTypeName drops a dotted qualifier (java.util.function.Function -> Function).
func simpleTypeName(s string) string {
	s = strings.TrimSpace(s)
	return s[strings.LastIndex(s, ".")+1:]
}

package springkt

import (
	"fmt"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/kotlin"
	"github.com/farhadamjady/service-discovery/internal/query"
)

// kafkaFor runs the Kafka detector over a Kotlin source with a DTO type index
// built across the source plus any payload-DTO sources (separate files, as in
// the real multi-file pipeline).
func kafkaFor(t *testing.T, src string, dtoSrcs ...string) *model.Service {
	t.Helper()
	main, err := kotlin.NewParser().Parse("Kafka.kt", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	files := []*kotlin.File{main.(*kotlin.File)}
	for i, d := range dtoSrcs {
		df, err := kotlin.NewParser().Parse(fmt.Sprintf("Dto%d.kt", i), []byte(d))
		if err != nil {
			t.Fatalf("parse dto %d: %v", i, err)
		}
		files = append(files, df.(*kotlin.File))
	}
	idx := &provider.Index{Types: kotlin.IndexTypes(files, nil)}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(main, []provider.Detector{kafkaDetector{}}, idx, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
	return svc
}

// TestKafkaConsumer proves K1's listener half: a @KafkaListener function emits a
// consumed-topic edge with the payload DTO's schema, skipping the @Header param.
func TestKafkaConsumer(t *testing.T) {
	src := `package x
@Service
class OrderConsumer {
    @KafkaListener(topics = ["orders"])
    fun handle(event: OrderEvent, @Header("k") key: String) { }
}`
	dto := `package x
data class OrderEvent(val id: Long, val status: String)`
	svc := kafkaFor(t, src, dto)

	if len(svc.KafkaConsumers) != 1 {
		t.Fatalf("consumers = %d, want 1", len(svc.KafkaConsumers))
	}
	c := svc.KafkaConsumers[0]
	if c.Topic != "orders" || !c.Resolved || c.Confidence != model.Confirmed {
		t.Errorf("consumer edge = %+v, want orders/confirmed/resolved", c)
	}
	if c.Schema == nil || c.Schema.Type != "OrderEvent" {
		t.Fatalf("consumer schema = %+v, want OrderEvent", c.Schema)
	}
	fields := map[string]string{}
	for _, f := range c.Schema.Nested {
		fields[f.Name] = f.Type
	}
	if fields["id"] != "integer" || fields["status"] != "string" {
		t.Errorf("payload fields = %v, want id:integer status:string", fields)
	}
}

// TestKafkaProducer proves K1's producer half: a KafkaTemplate<K, V>.send(topic,
// data) emits a produced-topic edge with the template's value type V as schema.
func TestKafkaProducer(t *testing.T) {
	src := `package x
@Service
class OrderProducer(private val kafkaTemplate: KafkaTemplate<String, OrderEvent>) {
    fun publish(e: OrderEvent) {
        kafkaTemplate.send("orders-out", e)
    }
}`
	dto := `package x
data class OrderEvent(val id: Long, val status: String)`
	svc := kafkaFor(t, src, dto)

	if len(svc.KafkaProducers) != 1 {
		t.Fatalf("producers = %d, want 1", len(svc.KafkaProducers))
	}
	p := svc.KafkaProducers[0]
	if p.Topic != "orders-out" || !p.Resolved || p.Confidence != model.Confirmed {
		t.Errorf("producer edge = %+v, want orders-out/confirmed/resolved", p)
	}
	if p.Schema == nil || p.Schema.Type != "OrderEvent" {
		t.Fatalf("producer schema = %+v, want OrderEvent", p.Schema)
	}
}

// TestKafkaNonTemplateSendIgnored proves the receiver-type guard: a send() on
// something that is not a KafkaTemplate is not a producer.
func TestKafkaNonTemplateSendIgnored(t *testing.T) {
	src := `package x
class Mailer(private val client: SmtpClient) {
    fun go() {
        client.send("hello", body)
    }
}`
	svc := kafkaFor(t, src)
	if len(svc.KafkaProducers) != 0 {
		t.Errorf("producers = %d, want 0 (client.send is not KafkaTemplate)", len(svc.KafkaProducers))
	}
}

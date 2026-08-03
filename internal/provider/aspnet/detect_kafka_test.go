package aspnet

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/csharp"
	"github.com/farhadamjady/service-discovery/internal/query"
)

// kafkaFor runs the Kafka detector over one C# source with the DTO type index
// built over it.
func kafkaFor(t *testing.T, src string) *model.Service {
	t.Helper()
	f, err := csharp.NewParser().Parse("K.cs", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	idx := &provider.Index{Types: buildTypeIndex([]*csharp.File{f.(*csharp.File)})}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{kafkaDetector{}}, idx, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
	return svc
}

// TestConfluentProducer proves K10's producer half: IProducer<K, V>.ProduceAsync
// emits a produced topic with V's schema (from a field-declared client).
func TestConfluentProducer(t *testing.T) {
	src := `
public class OrderEvent {
	public string OrderId { get; set; }
	public int Total { get; set; }
}
public class OrderService {
	private readonly IProducer<Null, OrderEvent> _producer;
	public async Task Publish(OrderEvent evt) {
		await _producer.ProduceAsync("orders-out", new Message<Null, OrderEvent> { Value = evt });
	}
}`
	svc := kafkaFor(t, src)
	if len(svc.KafkaProducers) != 1 {
		t.Fatalf("producers = %d, want 1", len(svc.KafkaProducers))
	}
	p := svc.KafkaProducers[0]
	if p.Topic != "orders-out" || !p.Resolved || p.Confidence != model.Confirmed {
		t.Errorf("producer = %+v, want orders-out/confirmed/resolved", p)
	}
	if p.Schema == nil || p.Schema.Type != "OrderEvent" {
		t.Fatalf("schema = %+v, want OrderEvent", p.Schema)
	}
	fields := map[string]string{}
	for _, f := range p.Schema.Nested {
		fields[f.Name] = f.Type
	}
	if fields["OrderId"] != "string" || fields["Total"] != "integer" {
		t.Errorf("payload fields = %v, want OrderId:string Total:integer", fields)
	}
}

// TestConfluentConsumer proves K10's consumer half: IConsumer<K, V>.Subscribe
// emits a consumed topic with V's schema (from a constructor-injected client).
func TestConfluentConsumer(t *testing.T) {
	src := `
public class OrderEvent {
	public string OrderId { get; set; }
}
public class OrderConsumer {
	private readonly IConsumer<Ignore, OrderEvent> _consumer;
	public OrderConsumer(IConsumer<Ignore, OrderEvent> consumer) { _consumer = consumer; }
	public void Listen() {
		_consumer.Subscribe("orders-in");
	}
}`
	svc := kafkaFor(t, src)
	if len(svc.KafkaConsumers) != 1 {
		t.Fatalf("consumers = %d, want 1", len(svc.KafkaConsumers))
	}
	c := svc.KafkaConsumers[0]
	if c.Topic != "orders-in" || !c.Resolved || c.Confidence != model.Confirmed {
		t.Errorf("consumer = %+v, want orders-in/confirmed/resolved", c)
	}
	if c.Schema == nil || c.Schema.Type != "OrderEvent" {
		t.Fatalf("schema = %+v, want OrderEvent", c.Schema)
	}
}

// TestNonKafkaProduceIgnored proves the receiver-type guard: a Produce/Subscribe
// on something that isn't an IProducer/IConsumer is not a Kafka edge.
func TestNonKafkaProduceIgnored(t *testing.T) {
	src := `
public class Widget {
	private readonly IWidgetFactory _producer;
	public void Go() { _producer.Produce("thing"); }
}`
	svc := kafkaFor(t, src)
	if len(svc.KafkaProducers) != 0 {
		t.Errorf("producers = %+v, want 0 (not an IProducer)", svc.KafkaProducers)
	}
}

package schema

import "github.com/farhadamjady/service-discovery/internal/model"

// ResolveKafka resolves a Kafka message payload type to a schema, files-first
// (DESIGN §12): a matching contract file (Avro/Proto/JSON Schema) wins as
// confirmed; otherwise an in-code DTO is walked and downgraded to likely; a
// scalar/opaque/unresolvable payload returns nil (safe-fail — the edge is kept,
// only the schema is dropped). Registry lookup is deferred (network).
func ResolveKafka(payloadType string, sources SchemaSources, types TypeSource) *model.Schema {
	name := kafkaPayloadName(payloadType)
	if name == "" || opaque[name] || scalarJSON[name] != "" {
		return nil // String/byte[]/Object payloads carry no structural schema
	}
	if sources != nil {
		if sch, ok := sources.Lookup(name); ok {
			return sch // confirmed, from the contract file
		}
	}
	if types != nil {
		if _, ok := types.Lookup(name); ok {
			if sch := NewWalker(types).Type(name); sch != nil {
				if sch.Confidence == model.Confirmed {
					sch.Confidence = model.Likely // in-code DTO is a likely Kafka schema
				}
				return sch
			}
		}
	}
	return nil // safe-fail
}

// kafkaPayloadName unwraps the transport wrappers around a message payload:
// ConsumerRecord<K,V> -> V, Message<T> -> T, List<T>/batch -> T.
func kafkaPayloadName(payloadType string) string {
	te := parseTypeExpr(payloadType)
	for i := 0; i < 5; i++ {
		switch {
		case te.Name == "ConsumerRecord" && len(te.Args) == 2:
			te = te.Args[1]
		case (te.Name == "Message" || arrayWrapper[te.Name]) && len(te.Args) == 1:
			te = te.Args[0]
		default:
			return te.Name
		}
	}
	return te.Name
}

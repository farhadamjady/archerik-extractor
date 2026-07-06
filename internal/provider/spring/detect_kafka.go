package spring

import (
	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
)

// kafkaDetector extracts Kafka edges: producers from KafkaTemplate.send call
// sites, consumers from @KafkaListener methods.
//
// Rules (to come): resolve the topic through the value resolver + Index.Config
// (placeholders, constants, deploy layer). The edge is ALWAYS emitted if real —
// schema resolution is separate enrichment and never drops an edge.
type kafkaDetector struct{}

func (kafkaDetector) Name() string             { return "spring.kafka" }
func (kafkaDetector) Protocol() model.Protocol { return model.ProtoKafka }
func (kafkaDetector) Rules() []provider.Rule   { return nil }

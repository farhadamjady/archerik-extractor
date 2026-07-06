package spring

import (
	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
)

// feignDetector extracts outbound HTTP dependencies from @FeignClient interfaces.
//
// Rules (to come): emit the raw logical name (name="payment-service") as
// TargetName — NOT a service_id. Resolve url="${payment.service.url}" through
// Index.Config; if it resolves, set URL + Confidence per the mapping table,
// else Resolved=false + Uncertain.
type feignDetector struct{}

func (feignDetector) Name() string             { return "spring.feign" }
func (feignDetector) Protocol() model.Protocol { return model.ProtoREST }
func (feignDetector) Rules() []provider.Rule   { return nil }

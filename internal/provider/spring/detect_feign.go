package spring

import (
	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
)

// feignDetector extracts outbound HTTP dependencies from @FeignClient interfaces.
//
// TODO(feign): emit the raw logical name (name="payment-service") as TargetName —
// NOT a service_id. Resolve url="${payment.service.url}" through ctx.Config; if it
// resolves, set URL + Confidence=Likely/Confirmed, else Resolved=false + Uncertain.
type feignDetector struct{}

func (feignDetector) Name() string { return "spring.feign" }

func (feignDetector) Detect(ctx *provider.ScanContext, svc *model.Service) error {
	return nil
}

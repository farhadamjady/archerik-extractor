package spring

import (
	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
)

// restTemplateDetector extracts outbound HTTP dependencies from RestTemplate
// call sites (getForObject / exchange / postForEntity / ...).
//
// Rules (to come): hand the URL-argument expression node to the value resolver;
// emit one edge per candidate (Conditional + CandidateGroup on multi-value).
type restTemplateDetector struct{}

func (restTemplateDetector) Name() string             { return "spring.resttemplate" }
func (restTemplateDetector) Protocol() model.Protocol { return model.ProtoREST }
func (restTemplateDetector) Rules() []provider.Rule   { return nil }

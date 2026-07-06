package spring

import (
	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
)

// webClientDetector extracts outbound HTTP dependencies from WebClient fluent
// chains (.baseUrl(...) + .uri(...)).
//
// Rules (to come): reconstruct the target through the value resolver's builder
// support; base URL and uri segments compose into one target.
type webClientDetector struct{}

func (webClientDetector) Name() string             { return "spring.webclient" }
func (webClientDetector) Protocol() model.Protocol { return model.ProtoREST }
func (webClientDetector) Rules() []provider.Rule   { return nil }

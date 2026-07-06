package spring

import (
	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
)

// restDetector extracts REST endpoints from @RestController classes.
//
// Rules (to come): one query capturing the class-level @RequestMapping and the
// method-level @GetMapping/@PostMapping/etc TOGETHER, so the handler composes
// the full path (class + method), keeps the HTTP verb, preserves path variables
// (/users/{id}), and never emits a method path in isolation.
type restDetector struct{}

func (restDetector) Name() string             { return "spring.rest" }
func (restDetector) Protocol() model.Protocol { return model.ProtoREST }
func (restDetector) Rules() []provider.Rule   { return nil }

package spring

import (
	"github.com/farhadamjady/super-discovery/internal/model"
	"github.com/farhadamjady/super-discovery/internal/provider"
)

// restDetector extracts REST endpoints from @RestController classes.
//
// TODO(rest): compose the endpoint path from the class-level @RequestMapping and
// the method-level @GetMapping/@PostMapping/etc. Keep the HTTP verb, preserve
// path variables (/users/{id}), and never emit a method path in isolation.
type restDetector struct{}

func (restDetector) Name() string { return "spring.rest" }

func (restDetector) Detect(ctx *provider.ScanContext, svc *model.Service) error {
	return nil
}

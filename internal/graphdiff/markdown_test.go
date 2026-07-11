package graphdiff

import (
	"strings"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
)

func TestMarkdownEmptyDiffRendersNothing(t *testing.T) {
	if md := Markdown(Compute(svc(nil), svc(nil))); md != "" {
		t.Errorf("empty diff must render empty string, got:\n%s", md)
	}
}

func TestMarkdownMixedDiff(t *testing.T) {
	head := svc(func(s *model.Service) {
		// add a dependency, remove the kafka producer, change a schema field
		s.OutboundDependencies = append(s.OutboundDependencies, model.Dependency{
			TargetName: "shipping-service", URL: "http://shipping:8080",
			Protocol: model.ProtoREST, Detection: model.DetectFeign,
			Confidence: model.Likely, Resolved: true,
		})
		s.KafkaProducers = nil
		s.Endpoints[0].Response.Nested[1].Type = "number" // total: integer -> number
	})
	md := Markdown(Compute(svc(nil), head))

	for _, want := range []string{
		"Architecture impact: **orders**",
		"**1 added · 1 removed · 1 changed**",
		"#### Outbound dependencies",
		"➕ shipping-service → `http://shipping:8080` · rest · feign · likely",
		"#### Kafka — produced topics",
		"➖ **orders.v1** · schema `OrderEvent` · kafka · confirmed",
		"#### Endpoints",
		"🔀 **GET /orders/{id}** — response_schema",
		"`response.total`: type changed integer → number",
		"<sub>service-discovery",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
}

func TestMarkdownUncertainWarning(t *testing.T) {
	head := svc(func(s *model.Service) {
		s.OutboundDependencies[0].Confidence = model.Uncertain
		s.OutboundDependencies[0].Resolved = false
	})
	md := Markdown(Compute(svc(nil), head))
	if !strings.Contains(md, "confidence: likely → uncertain ⚠️") {
		t.Errorf("uncertain movement must warn:\n%s", md)
	}
}

func TestMarkdownFirstScanCapped(t *testing.T) {
	// Empty baseline: everything is "added" — the section must cap.
	base := model.NewService("orders", "orders", "repo")
	head := svc(func(s *model.Service) {
		for i := 0; i < 30; i++ {
			s.Endpoints = append(s.Endpoints, model.Endpoint{
				Method: "GET", Path: "/x/" + strings.Repeat("a", i+1),
				Protocol: model.ProtoREST, Detection: model.DetectAnnotation, Confidence: model.Confirmed,
			})
		}
	})
	md := Markdown(Compute(base, head))
	// the cap is PER SECTION — measure inside the Endpoints section only
	section := md[strings.Index(md, "#### Endpoints"):]
	if next := strings.Index(section[5:], "####"); next >= 0 {
		section = section[:next+5]
	}
	if got := strings.Count(section, "➕"); got > maxItemsPerSection {
		t.Errorf("rendered %d added endpoint lines, cap is %d", got, maxItemsPerSection)
	}
	if !strings.Contains(md, "…and") {
		t.Errorf("cap note missing:\n%s", md)
	}
}

func TestMarkdownDeterministic(t *testing.T) {
	head := svc(func(s *model.Service) { s.Endpoints[0].Confidence = model.Likely })
	a := Markdown(Compute(svc(nil), head))
	b := Markdown(Compute(svc(nil), head))
	if a != b {
		t.Error("markdown not deterministic")
	}
}

package spring

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/query"
)

// feignDeps runs the Feign detector over src with the given resolver (nil for
// cases needing no config) and returns the outbound dependencies, sorted.
func feignDeps(t *testing.T, src string, cfg provider.ConfigResolver) []model.Dependency {
	t.Helper()
	f, err := java.NewParser().Parse("P.java", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	svc := model.NewService("s", "s", "")
	idx := &provider.Index{Config: cfg}
	if err := query.New().Run(f, []provider.Detector{feignDetector{}}, idx, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
	return svc.OutboundDependencies
}

func one(t *testing.T, deps []model.Dependency) model.Dependency {
	t.Helper()
	if len(deps) != 1 {
		t.Fatalf("got %d dependencies, want 1: %+v", len(deps), deps)
	}
	return deps[0]
}

func TestFeignNameForms(t *testing.T) {
	for _, src := range []string{
		`@FeignClient("payment-service") interface P {}`,
		`@FeignClient(name = "payment-service") interface P {}`,
		`@FeignClient(value = "payment-service") interface P {}`,
	} {
		d := one(t, feignDeps(t, src, nil))
		if d.TargetName != "payment-service" {
			t.Errorf("%s: TargetName = %q", src, d.TargetName)
		}
		if !d.Resolved || d.Confidence != model.Confirmed {
			t.Errorf("%s: resolved=%v conf=%s, want true/confirmed", src, d.Resolved, d.Confidence)
		}
		if d.Protocol != model.ProtoREST || d.Detection != model.DetectFeign {
			t.Errorf("%s: edge fields = (%s,%s)", src, d.Protocol, d.Detection)
		}
		if d.URL != "" {
			t.Errorf("%s: name-only client should have no URL, got %q", src, d.URL)
		}
	}
}

func TestFeignLiteralURL(t *testing.T) {
	d := one(t, feignDeps(t, `@FeignClient(name="p", url="http://localhost:8080") interface P {}`, nil))
	if d.URL != "http://localhost:8080" || d.Confidence != model.Confirmed || !d.Resolved {
		t.Errorf("literal url = %+v, want url set/confirmed/resolved", d)
	}
	if d.TargetName != "p" {
		t.Errorf("TargetName = %q, want p", d.TargetName)
	}
}

func TestFeignPlaceholderViaConfig(t *testing.T) {
	cfg := buildStore(t, nil, map[string]string{
		"application.yml": "payment:\n  url: http://payment:8080\n",
	})
	d := one(t, feignDeps(t, `@FeignClient(name="p", url="${payment.url}") interface P {}`, cfg))
	if d.URL != "http://payment:8080" || d.Confidence != model.Likely || !d.Resolved {
		t.Errorf("config-resolved url = %+v, want resolved/likely", d)
	}
}

func TestFeignPlaceholderViaHelm(t *testing.T) {
	// URL exists only in a Helm chart (values -> template env), not application.*.
	cfg := buildLayered(t, "", map[string]string{
		"application.yml":   "spring:\n  application:\n    name: cart\n",
		"chart/values.yaml": "payment:\n  serviceUrl: http://payment:9000\n",
		"chart/templates/deployment.yaml": "" +
			"        - name: PAYMENT_SERVICE_URL\n" +
			"          value: {{ .Values.payment.serviceUrl }}\n",
	})
	d := one(t, feignDeps(t, `@FeignClient(name="p", url="${PAYMENT_SERVICE_URL}") interface P {}`, cfg))
	if d.URL != "http://payment:9000" || d.Confidence != model.Likely {
		t.Errorf("helm-resolved url = %+v, want http://payment:9000 / likely", d)
	}
}

func TestFeignUnresolvedKeepsName(t *testing.T) {
	cfg := buildStore(t, nil, map[string]string{"application.yml": "a: 1\n"})
	d := one(t, feignDeps(t, `@FeignClient(name="payment", url="${runtime.only}") interface P {}`, cfg))
	if d.Resolved || d.Confidence != model.Uncertain {
		t.Errorf("unresolved url: resolved=%v conf=%s, want false/uncertain", d.Resolved, d.Confidence)
	}
	// The logical name is still emitted even when the url can't be pinned.
	if d.TargetName != "payment" {
		t.Errorf("TargetName = %q, want payment", d.TargetName)
	}
	if d.URL != "" {
		t.Errorf("URL = %q, want empty", d.URL)
	}
}

func TestFeignDivergentOverlaysCandidates(t *testing.T) {
	cfg := buildLayered(t, "", map[string]string{
		"values.yaml":         "payment:\n  url: http://base\n",
		"values-staging.yaml": "payment:\n  url: http://staging\n",
		"values-prod.yaml":    "payment:\n  url: http://prod\n",
	})
	deps := feignDeps(t, `@FeignClient(name="p", url="${payment.url}") interface P {}`, cfg)
	if len(deps) != 3 {
		t.Fatalf("got %d candidate edges, want 3: %+v", len(deps), deps)
	}
	group := deps[0].CandidateGroup
	if group == "" {
		t.Error("candidate edges must share a non-empty CandidateGroup")
	}
	for _, d := range deps {
		if !d.Conditional || d.CandidateGroup != group {
			t.Errorf("candidate = %+v, want conditional + shared group %q", d, group)
		}
		if d.Confidence != model.Likely {
			t.Errorf("candidate confidence = %s, want likely (capped)", d.Confidence)
		}
	}
}

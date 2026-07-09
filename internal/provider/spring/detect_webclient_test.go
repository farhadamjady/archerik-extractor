package spring

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
)

func wcDeps(t *testing.T, src string) []model.Dependency {
	t.Helper()
	return httpDeps(t, webClientDetector{}, nil, "class C { WebClient wc; void m(String id) {\n"+src+"\n} }")
}

func hasURL(deps []model.Dependency, url string) bool {
	for _, d := range deps {
		if d.URL == url || d.TargetName == url {
			return true
		}
	}
	return false
}

func TestWebClientBaseUrl(t *testing.T) {
	deps := wcDeps(t, `WebClient.builder().baseUrl("http://payment").build();`)
	if !hasURL(deps, "http://payment") {
		t.Errorf("baseUrl not captured: %+v", deps)
	}
	for _, d := range deps {
		if d.Detection != model.DetectWebClient || d.Protocol != model.ProtoREST {
			t.Errorf("edge fields = (%s,%s)", d.Protocol, d.Detection)
		}
	}
}

func TestWebClientCreate(t *testing.T) {
	deps := wcDeps(t, `WebClient.create("http://orders");`)
	if len(deps) != 1 || deps[0].URL != "http://orders" || !deps[0].Resolved {
		t.Errorf("create = %+v, want http://orders resolved", deps)
	}
}

func TestWebClientComposeBaseAndURI(t *testing.T) {
	// Same chain: create(base) ... uri(path) -> composed edge base+path.
	deps := wcDeps(t, `WebClient.create("http://payment").get().uri("/pay/{id}").retrieve();`)
	if !hasURL(deps, "http://payment/pay/{id}") {
		t.Errorf("composed base+uri missing: %+v", deps)
	}
}

func TestWebClientTemplatedURI(t *testing.T) {
	// base is known, uri concatenates a param -> Template with a hole.
	deps := wcDeps(t, `WebClient.create("http://payment").get().uri("/pay/" + id).retrieve();`)
	if !hasURL(deps, "http://payment/pay/{?}") {
		t.Errorf("templated compose missing: %+v", deps)
	}
}

func TestWebClientFullURLURIWithoutBase(t *testing.T) {
	// A uri that is itself an absolute URL, on a stored client (no in-chain base).
	deps := wcDeps(t, `wc.get().uri("http://other/api").retrieve();`)
	if !hasURL(deps, "http://other/api") {
		t.Errorf("full-URL uri missing: %+v", deps)
	}
}

func TestWebClientBarePathURISkipped(t *testing.T) {
	// A bare path with no in-chain base is relative to a base captured elsewhere.
	deps := wcDeps(t, `wc.get().uri("/pay").retrieve();`)
	for _, d := range deps {
		if d.TargetName == "/pay" || d.URL == "/pay" {
			t.Errorf("bare-path uri should be skipped, got %+v", deps)
		}
	}
}

package quarkus

import (
	"fmt"
	"sort"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/query"
)

func scanSrc(t *testing.T, src string, dets ...provider.Detector) *model.Service {
	t.Helper()
	f, err := java.NewParser().Parse("C.java", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	jf := f.(*java.File)
	idx := &provider.Index{}
	idx.Symbols = java.IndexSymbols([]*java.File{jf})
	idx.Types = java.IndexTypes([]*java.File{jf}, nil)
	contracts := map[string][]provider.ASTNode{}
	indexContractsIn(jf, contracts)
	if len(contracts) > 0 {
		idx.HTTPContracts = contracts
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, dets, idx, java.NewEvaluator(idx), svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
	return svc
}

func endpoints(t *testing.T, src string) []string {
	svc := scanSrc(t, src, restDetector{})
	var out []string
	for _, e := range svc.Endpoints {
		out = append(out, fmt.Sprintf("%s %s", e.Method, e.Path))
	}
	sort.Strings(out)
	return out
}

func TestRESTEndpoints(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "class @Path + separate verb + method @Path, path var preserved",
			src: `import jakarta.ws.rs.*;
				@Path("/heroes")
				class HeroResource {
					@GET @Path("/{id}") Hero get(@PathParam("id") Long id) { return null; }
					@POST Hero create(Hero h) { return null; }
					@GET Hero list() { return null; }
				}`,
			want: []string{"GET /heroes", "GET /heroes/{id}", "POST /heroes"},
		},
		{
			name: "method without a verb annotation is not an endpoint",
			src: `@Path("/x") class X {
					@Path("/sub") Object subLocator() { return null; }
					@DELETE void del() {}
				}`,
			want: []string{"DELETE /x"},
		},
		{
			name: "non-resource class ignored",
			src: `@ApplicationScoped class Service {
					@GET String s() { return null; }
				}`,
			want: nil,
		},
		{
			// API-interface pattern (#41): verbs/paths on the implemented interface.
			name: "resource implements interface with the mappings",
			src: `import jakarta.ws.rs.*;
				interface HeroesApi {
					@GET @Path("/random") Hero random();
					@POST Hero create(Hero h);
				}
				@Path("/api/heroes")
				class HeroResource implements HeroesApi {
					public Hero random() { return null; }
					public Hero create(Hero h) { return null; }
				}`,
			want: []string{"GET /api/heroes/random", "POST /api/heroes"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := endpoints(t, tc.src)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRestClient(t *testing.T) {
	cases := []struct {
		name       string
		src        string
		target     string
		url        string
		resolved   bool
		confidence model.Confidence
	}{
		{
			name:       "configKey logical target",
			src:        `@RegisterRestClient(configKey = "hero-client") interface HeroClient { @GET Object r(); }`,
			target:     "hero-client",
			resolved:   true,
			confidence: model.Confirmed,
		},
		{
			name:       "baseUri absolute URL -> host",
			src:        `@RegisterRestClient(baseUri = "http://heroes:8080") interface HeroClient { @GET Object r(); }`,
			target:     "heroes:8080",
			url:        "http://heroes:8080",
			resolved:   true,
			confidence: model.Confirmed,
		},
		{
			name:       "bare @RegisterRestClient -> interface name",
			src:        `@RegisterRestClient interface NarrationClient { @GET Object r(); }`,
			target:     "NarrationClient",
			resolved:   true,
			confidence: model.Confirmed,
		},
		{
			name:       "baseUri placeholder -> uncertain",
			src:        `@RegisterRestClient(baseUri = "${narration.url}") interface C { @GET Object r(); }`,
			target:     "",
			resolved:   false,
			confidence: model.Uncertain,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := scanSrc(t, tc.src, restClientDetector{})
			if len(svc.OutboundDependencies) != 1 {
				t.Fatalf("got %d deps, want 1: %+v", len(svc.OutboundDependencies), svc.OutboundDependencies)
			}
			d := svc.OutboundDependencies[0]
			if d.TargetName != tc.target || d.URL != tc.url || d.Resolved != tc.resolved || d.Confidence != tc.confidence {
				t.Errorf("got target=%q url=%q resolved=%v conf=%q; want target=%q url=%q resolved=%v conf=%q",
					d.TargetName, d.URL, d.Resolved, d.Confidence, tc.target, tc.url, tc.resolved, tc.confidence)
			}
			if d.Detection != model.DetectMPRestClient {
				t.Errorf("detection=%q", d.Detection)
			}
		})
	}
}

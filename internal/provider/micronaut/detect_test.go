package micronaut

import (
	"fmt"
	"sort"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/query"
)

// scanSrc runs the given detectors over one Java source through the REAL query
// engine + REAL parser + evaluator, and returns the resulting Service.
func scanSrc(t *testing.T, src string, dets ...provider.Detector) *model.Service {
	t.Helper()
	f, err := java.NewParser().Parse("C.java", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	idx := &provider.Index{}
	// Build the type/symbol index over this single file so schemas resolve.
	jf := f.(*java.File)
	idx.Symbols = java.IndexSymbols([]*java.File{jf})
	idx.Types = java.IndexTypes([]*java.File{jf}, nil)
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
			name: "controller base + method path, path variable preserved",
			src: `import io.micronaut.http.annotation.*;
				@Controller("/pets")
				class PetController {
					@Get("/{id}") String get() { return null; }
					@Post String create() { return null; }
				}`,
			want: []string{"GET /pets/{id}", "POST /pets"},
		},
		{
			name: "no controller base path",
			src: `@Controller
				class H {
					@Get("/health") String ping() { return "ok"; }
				}`,
			want: []string{"GET /health"},
		},
		{
			name: "uri= named attribute and array uris=",
			src: `@Controller("/api")
				class C {
					@Delete(uri = "/items/{id}") void del() {}
					@Get(uris = {"/a", "/b"}) String multi() { return null; }
				}`,
			want: []string{"DELETE /api/items/{id}", "GET /api/a", "GET /api/b"},
		},
		{
			name: "non-controller class ignored",
			src: `@Singleton class Service {
					@Get("/x") String s() { return null; }
				}`,
			want: nil,
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

func httpDeps(t *testing.T, src string) []model.Dependency {
	svc := scanSrc(t, src, clientDetector{})
	return svc.OutboundDependencies
}

func TestClient(t *testing.T) {
	cases := []struct {
		name       string
		src        string
		target     string
		url        string
		resolved   bool
		confidence model.Confidence
	}{
		{
			name:       "logical service id (positional)",
			src:        `@Client("catalogue") interface CatalogueClient { @Get String all(); }`,
			target:     "catalogue",
			resolved:   true,
			confidence: model.Confirmed,
		},
		{
			name:       "id= attribute",
			src:        `@Client(id = "inventory") interface Inv { @Get String all(); }`,
			target:     "inventory",
			resolved:   true,
			confidence: model.Confirmed,
		},
		{
			name:       "absolute URL -> host target",
			src:        `@Client("http://payments:8080") interface Pay { @Get String p(); }`,
			target:     "payments:8080",
			url:        "http://payments:8080",
			resolved:   true,
			confidence: model.Confirmed,
		},
		{
			name:       "relative path -> anonymous uncertain",
			src:        `@Client("/pets") interface Self { @Get String p(); }`,
			target:     "",
			url:        "/pets",
			resolved:   false,
			confidence: model.Uncertain,
		},
		{
			name:       "placeholder -> uncertain (no config)",
			src:        `@Client("${catalogue.url}") interface C { @Get String p(); }`,
			target:     "",
			resolved:   false,
			confidence: model.Uncertain,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := httpDeps(t, tc.src)
			if len(deps) != 1 {
				t.Fatalf("got %d deps, want 1: %+v", len(deps), deps)
			}
			d := deps[0]
			if d.TargetName != tc.target || d.URL != tc.url || d.Resolved != tc.resolved || d.Confidence != tc.confidence {
				t.Errorf("got target=%q url=%q resolved=%v conf=%q; want target=%q url=%q resolved=%v conf=%q",
					d.TargetName, d.URL, d.Resolved, d.Confidence, tc.target, tc.url, tc.resolved, tc.confidence)
			}
			if d.Detection != model.DetectMicronautClient || d.Protocol != model.ProtoREST {
				t.Errorf("detection=%q protocol=%q", d.Detection, d.Protocol)
			}
		})
	}
}

func TestKafkaProducer(t *testing.T) {
	svc := scanSrc(t, `import io.micronaut.configuration.kafka.annotation.*;
		@KafkaClient interface ProductClient {
			@Topic("products") void send(@KafkaKey String id, Product product);
		}`, kafkaDetector{})
	if len(svc.KafkaProducers) != 1 || len(svc.KafkaConsumers) != 0 {
		t.Fatalf("producers=%d consumers=%d", len(svc.KafkaProducers), len(svc.KafkaConsumers))
	}
	p := svc.KafkaProducers[0]
	if p.Topic != "products" || !p.Resolved || p.Confidence != model.Confirmed {
		t.Errorf("producer = %+v", p)
	}
}

func TestKafkaConsumer(t *testing.T) {
	svc := scanSrc(t, `import io.micronaut.configuration.kafka.annotation.*;
		@KafkaListener(groupId = "g") class ProductListener {
			@Topic("products") void receive(Product product) {}
		}`, kafkaDetector{})
	if len(svc.KafkaConsumers) != 1 || len(svc.KafkaProducers) != 0 {
		t.Fatalf("producers=%d consumers=%d", len(svc.KafkaProducers), len(svc.KafkaConsumers))
	}
	c := svc.KafkaConsumers[0]
	if c.Topic != "products" || !c.Resolved || c.Confidence != model.Confirmed {
		t.Errorf("consumer = %+v", c)
	}
}

func TestKafkaConsumerMultiTopic(t *testing.T) {
	svc := scanSrc(t, `@KafkaListener class L {
			@Topic({"a", "b"}) void receive(String msg) {}
		}`, kafkaDetector{})
	got := map[string]bool{}
	for _, c := range svc.KafkaConsumers {
		got[c.Topic] = true
	}
	if !got["a"] || !got["b"] || len(svc.KafkaConsumers) != 2 {
		t.Errorf("consumers = %+v", svc.KafkaConsumers)
	}
}

func TestKafkaDynamicTopicParam(t *testing.T) {
	// @Topic on a parameter -> runtime destination -> uncertain producer edge.
	svc := scanSrc(t, `@KafkaClient interface C {
			void send(@Topic String topic, String msg);
		}`, kafkaDetector{})
	if len(svc.KafkaProducers) != 1 {
		t.Fatalf("producers=%d", len(svc.KafkaProducers))
	}
	if p := svc.KafkaProducers[0]; p.Resolved || p.Confidence != model.Uncertain {
		t.Errorf("expected uncertain unresolved edge, got %+v", p)
	}
}

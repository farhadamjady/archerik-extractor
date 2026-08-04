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

func TestJaxrsClient(t *testing.T) {
	cases := []struct {
		name       string
		src        string
		expect     bool // an edge is expected
		target     string
		url        string
		resolved   bool
		confidence model.Confidence
	}{
		{
			name:       "literal absolute base URL -> host confirmed",
			src:        `class VillainClient { Object c = ClientBuilder.newClient().target("http://villains:8080/api").path("random"); }`,
			expect:     true,
			target:     "villains:8080",
			url:        "http://villains:8080/api",
			resolved:   true,
			confidence: model.Confirmed,
		},
		{
			name:       "config-accessor base -> enclosing bean name, uncertain",
			src:        `class VillainClient { VillainClient(FightConfig cfg) { this.c = ClientBuilder.newClient().register(f).target(cfg.villain().clientBaseUrl()).path("api/villains/"); } }`,
			expect:     true,
			target:     "VillainClient",
			confidence: model.Uncertain,
		},
		{
			name:       "newBuilder().build() chain -> detected",
			src:        `class C { Object c = ClientBuilder.newBuilder().build().target("http://loc:9/api"); }`,
			expect:     true,
			target:     "loc:9",
			url:        "http://loc:9/api",
			resolved:   true,
			confidence: model.Confirmed,
		},
		{
			name:   "target() not rooted at ClientBuilder -> ignored",
			src:    `class C { Object f(jakarta.ws.rs.client.WebTarget wt) { return wt.target("random"); } }`,
			expect: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := scanSrc(t, tc.src, jaxrsClientDetector{})
			if !tc.expect {
				if len(svc.OutboundDependencies) != 0 {
					t.Fatalf("want no deps, got %+v", svc.OutboundDependencies)
				}
				return
			}
			if len(svc.OutboundDependencies) != 1 {
				t.Fatalf("got %d deps, want 1: %+v", len(svc.OutboundDependencies), svc.OutboundDependencies)
			}
			d := svc.OutboundDependencies[0]
			if d.TargetName != tc.target || d.URL != tc.url || d.Resolved != tc.resolved || d.Confidence != tc.confidence {
				t.Errorf("got target=%q url=%q resolved=%v conf=%q; want target=%q url=%q resolved=%v conf=%q",
					d.TargetName, d.URL, d.Resolved, d.Confidence, tc.target, tc.url, tc.resolved, tc.confidence)
			}
			if d.Detection != model.DetectJaxrsClient || d.Protocol != model.ProtoREST {
				t.Errorf("detection=%q protocol=%q", d.Detection, d.Protocol)
			}
		})
	}
}

// scanMessaging runs the messaging detector with a config store built from a
// properties string, exercising channel→topic mapping + connector gating.
func scanMessaging(t *testing.T, src, props string) *model.Service {
	t.Helper()
	f, err := java.NewParser().Parse("C.java", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	jf := f.(*java.File)
	idx := &provider.Index{}
	idx.Symbols = java.IndexSymbols([]*java.File{jf})
	idx.Types = java.IndexTypes([]*java.File{jf}, nil)
	cfg := &flatConfig{values: map[string]string{}}
	parseProperties([]byte(props), cfg.values)
	idx.Config = cfg
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{messagingDetector{}}, idx, java.NewEvaluator(idx), svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
	return svc
}

func TestMessaging(t *testing.T) {
	// @Incoming channel "input" maps to topic "notification" via config; the
	// @Channel Emitter "notification" is a Kafka producer; an in-memory channel
	// (no connector) must NOT become a Kafka edge.
	src := `import org.eclipse.microprofile.reactive.messaging.*;
		class NotificationService {
			public NotificationService(@Channel("notification") Emitter<NotifDTO> emitter) {}
			@Incoming("input") public void consume(NotifDTO n) {}
			@Outgoing("ui-updates") public String toUi() { return null; }
		}`
	props := "" +
		"mp.messaging.outgoing.notification.connector=smallrye-kafka\n" +
		"mp.messaging.outgoing.notification.topic=notification\n" +
		"mp.messaging.incoming.input.connector=smallrye-kafka\n" +
		"mp.messaging.incoming.input.topic=notification\n"
	// ui-updates has no connector -> in-memory -> skipped.
	svc := scanMessaging(t, src, props)

	gotP := topics(svc.KafkaProducers)
	gotC := topics(svc.KafkaConsumers)
	if len(gotP) != 1 || gotP[0] != "notification" {
		t.Errorf("producers = %v, want [notification]", gotP)
	}
	if len(gotC) != 1 || gotC[0] != "notification" {
		t.Errorf("consumers = %v, want [notification] (input channel -> notification topic)", gotC)
	}
	for _, e := range append(svc.KafkaProducers, svc.KafkaConsumers...) {
		if e.Detection != model.DetectReactiveMessaging || e.Protocol != model.ProtoKafka {
			t.Errorf("edge detection/protocol = %q/%q", e.Detection, e.Protocol)
		}
	}
}

func TestMessagingChannelConstant(t *testing.T) {
	// Channel name is a static-final constant; topic defaults to the channel.
	src := `class SuperStats {
			static final String FIGHTS = "fights";
			@Incoming(FIGHTS) public void consume(Fight f) {}
		}`
	props := "mp.messaging.incoming.fights.connector=smallrye-kafka\n"
	svc := scanMessaging(t, src, props)
	if got := topics(svc.KafkaConsumers); len(got) != 1 || got[0] != "fights" {
		t.Errorf("consumers = %v, want [fights]", got)
	}
}

func topics(edges []model.KafkaEdge) []string {
	var out []string
	for _, e := range edges {
		out = append(out, e.Topic)
	}
	return out
}

// TestJaxrsResponseBodyInference proves #64: a handler returning the typeless
// javax.ws.rs.core.Response gets its body from the entity/ok chain, resolving the
// payload method's return type via the field + method-return indexes. Page<T> and
// List<T> unwrap; a bodyless Response.ok().build() stays empty.
func TestJaxrsResponseBodyInference(t *testing.T) {
	src := `
public interface OrderService {
	Page<OrderDTO> findAll(Pageable p);
	List<OrderDTO> findMine();
}
public class OrderDTO { public String id; public String name; }
@Path("/api")
public class OrderResource {
	private final OrderService orderService;
	@GET @Path("/orders")
	public Response findAll(@BeanParam Pageable p) {
		return Response.ok().entity(orderService.findAll(p)).build();
	}
	@GET @Path("/orders/me")
	public Response findMine() {
		return Response.ok(orderService.findMine()).build();
	}
	@DELETE @Path("/orders/{id}")
	public Response delete(@PathParam("id") long id) {
		return Response.ok().build();
	}
}`
	jf := mustParseJava(t, src)
	idx := &provider.Index{}
	idx.Types = java.IndexTypes([]*java.File{jf}, nil)
	if err := (methodIndexer{}).Index(&provider.IndexContext{Parsed: map[string]provider.ParsedFile{"C.java": jf}}, idx); err != nil {
		t.Fatalf("methodIndexer: %v", err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(jf, []provider.Detector{restDetector{}}, idx, java.NewEvaluator(idx), svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	byPath := map[string]model.Endpoint{}
	for _, e := range svc.Endpoints {
		byPath[e.Method+" "+e.Path] = e
	}
	if r := byPath["GET /api/orders"].Response; r == nil || r.Type != "OrderDTO" {
		t.Errorf("GET /api/orders resp = %+v, want OrderDTO (Page<OrderDTO> unwrapped)", r)
	}
	if r := byPath["GET /api/orders/me"].Response; r == nil || r.Type != "array" || r.Items != "OrderDTO" {
		t.Errorf("GET /api/orders/me resp = %+v, want array of OrderDTO (List<OrderDTO>)", r)
	}
	if r := byPath["DELETE /api/orders/{id}"].Response; r != nil {
		t.Errorf("DELETE resp = %+v, want nil (Response.ok().build(), no entity)", r)
	}
}

func mustParseJava(t *testing.T, src string) *java.File {
	t.Helper()
	f, err := java.NewParser().Parse("C.java", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return f.(*java.File)
}

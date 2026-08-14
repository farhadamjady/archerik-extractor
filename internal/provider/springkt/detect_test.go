package springkt

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/kotlin"
	"github.com/farhadamjady/archerik-extractor/internal/query"
	"github.com/farhadamjady/archerik-extractor/internal/scan"
)

var _ provider.Provider = (*Provider)(nil)

// endpoints runs the REST detector over one Kotlin source through the real query
// engine + Kotlin parser and returns "VERB PATH" strings.
func endpoints(t *testing.T, src string) []string {
	t.Helper()
	f, err := kotlin.NewParser().Parse("C.kt", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{restDetector{}}, &provider.Index{}, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
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
			name: "class base + method path, path variable preserved",
			src: `package x
				@RestController
				@RequestMapping("/api/v1")
				class UserController(val repo: UserRepository) {
					@GetMapping("/users/{id}")
					fun get(@PathVariable id: Int): User = repo.findById(id)
					@PostMapping("/users")
					fun create(@RequestBody u: User): User = repo.save(u)
				}`,
			want: []string{"GET /api/v1/users/{id}", "POST /api/v1/users"},
		},
		{
			name: "no class mapping; method mapping with no path maps to base",
			src: `package x
				@RestController
				class H {
					@GetMapping
					fun ping(): String = "ok"
				}`,
			want: []string{"GET /"},
		},
		{
			name: "expression-body and block-body both detected",
			src: `package x
				@RestController @RequestMapping("/orders")
				class OrderController {
					@DeleteMapping("/{id}")
					fun del(@PathVariable id: Long) { }
					@GetMapping
					fun all(): List<Order> = emptyList()
				}`,
			want: []string{"DELETE /orders/{id}", "GET /orders"},
		},
		{
			name: "non-controller class ignored",
			src: `package x
				@Service
				class UserService {
					@GetMapping("/x") fun s(): String = ""
				}`,
			want: nil,
		},
		{
			name: "plain @Controller only @ResponseBody methods are REST",
			src: `package x
				@Controller
				class MixedController {
					@GetMapping("/api/data") @ResponseBody
					fun data(): String = ""
					@GetMapping("/page")
					fun page(): String = "view"
				}`,
			want: []string{"GET /api/data"},
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

func TestDetectors(t *testing.T) {
	want := map[string]model.Protocol{
		"springkt.rest":   model.ProtoREST,
		"springkt.client": model.ProtoREST,
		"springkt.kafka":  model.ProtoKafka,
	}
	dets := New().Detectors()
	if len(dets) != len(want) {
		t.Fatalf("got %d detectors, want %d", len(dets), len(want))
	}
	for _, d := range dets {
		if want[d.Name()] != d.Protocol() {
			t.Errorf("detector %q protocol %q", d.Name(), d.Protocol())
		}
	}
}

func TestMatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "build.gradle.kts", `dependencies { implementation("org.springframework.boot:spring-boot-starter-web") }`)
	writeFile(t, root, "src/main/kotlin/App.kt", "@SpringBootApplication\nclass App")
	fs := scan.NewOSFileTree(root, nil)
	matched, score := New().Match(root, fs)
	if !matched || score != 7 { // kt(1) + spring-boot(3) + @SpringBootApplication(3)
		t.Fatalf("matched=%v score=%d, want matched=true score=7", matched, score)
	}
	// A pure-Java repo must not match (no .kt).
	javaRepo := t.TempDir()
	writeFile(t, javaRepo, "src/main/java/App.java", "@SpringBootApplication class App {}")
	if m, _ := New().Match(javaRepo, scan.NewOSFileTree(javaRepo, nil)); m {
		t.Error("Kotlin provider must not match a pure-Java repo")
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

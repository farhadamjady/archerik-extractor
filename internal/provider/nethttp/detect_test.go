package nethttp

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/golang"
	"github.com/farhadamjady/archerik-extractor/internal/query"
	"github.com/farhadamjady/archerik-extractor/internal/scan"
)

var _ provider.Provider = (*Provider)(nil)

func endpoints(t *testing.T, src string) []string {
	t.Helper()
	f, err := golang.NewParser().Parse("srv.go", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{routeDetector{}}, &provider.Index{}, nil, svc); err != nil {
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

func TestRoutes(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "Go 1.22 method patterns + wildcards, method-less -> *",
			src: `package main
				import "net/http"
				func main() {
					mux := http.NewServeMux()
					mux.HandleFunc("GET /items/{id}", getItem)
					mux.HandleFunc("POST /items", createItem)
					mux.HandleFunc("/health", health)
					http.Handle("/static/", fs)
				}`,
			// net/http keeps the trailing slash meaningful: "/static/" is a
			// subtree (prefix) route, distinct from an exact "/static".
			want: []string{"* /health", "* /static/", "GET /items/{id}", "POST /items"},
		},
		{
			name: "trailing wildcard normalized, {$} dropped, host stripped",
			src: `package main
				import "net/http"
				func main() {
					mux.HandleFunc("GET /files/{path...}", serve)
					mux.HandleFunc("GET example.com/exact/{$}", exact)
				}`,
			want: []string{"GET /exact", "GET /files/{path}"},
		},
		{
			name: "no net/http import -> nothing (avoids foreign .Handle/.HandleFunc)",
			src: `package main
				func main() {
					bus.HandleFunc("/not-a-route", h)
					queue.Handle("/x", h)
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

func TestDetectors(t *testing.T) {
	want := map[string]model.Protocol{
		"nethttp.route":  model.ProtoREST,
		"nethttp.client": model.ProtoREST,
		"nethttp.kafka":  model.ProtoKafka,
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
	writeFile(t, root, "go.mod", "module example.com/svc\n\ngo 1.22")
	writeFile(t, root, "main.go", "package main\nimport \"net/http\"\nfunc main(){ http.HandleFunc(\"/x\", h); http.ListenAndServe(\":8080\", nil) }")
	m, score := New().Match(root, scan.NewOSFileTree(root, nil))
	if !m || score != 6 { // go(1) + net/http import(3) + routing call(2)
		t.Fatalf("matched=%v score=%d, want true/6", m, score)
	}
	// A Go repo that never imports net/http must not match.
	other := t.TempDir()
	writeFile(t, other, "main.go", "package main\nfunc main(){}")
	if m, _ := New().Match(other, scan.NewOSFileTree(other, nil)); m {
		t.Error("must not match a non-net/http Go repo")
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

func outbound(t *testing.T, src string) []model.Dependency {
	t.Helper()
	f, err := golang.NewParser().Parse("client.go", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{clientDetector{}}, &provider.Index{}, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
	return svc.OutboundDependencies
}

func TestOutboundClient(t *testing.T) {
	src := `package main
		import "net/http"
		func call() {
			http.Get("http://catalog-service/items")
			http.Post("http://payment:9000/charge", "application/json", body)
			req, _ := http.NewRequest("DELETE", "http://inventory/items/1", nil)
			http.Get(dynamicURL)
		}`
	deps := outbound(t, src)
	if len(deps) != 4 {
		t.Fatalf("got %d deps, want 4: %+v", len(deps), deps)
	}
	byTarget := map[string]model.Dependency{}
	uncertain := 0
	for _, d := range deps {
		if d.TargetName != "" {
			byTarget[d.TargetName] = d
		} else {
			uncertain++
		}
		if d.Detection != model.DetectHTTPClient || d.Protocol != model.ProtoREST {
			t.Errorf("detection/protocol = %q/%q", d.Detection, d.Protocol)
		}
	}
	for _, want := range []string{"catalog-service", "payment:9000", "inventory"} {
		if d, ok := byTarget[want]; !ok || !d.Resolved || d.Confidence != model.Confirmed {
			t.Errorf("target %q missing or not confirmed: %+v", want, byTarget[want])
		}
	}
	if uncertain != 1 {
		t.Errorf("dynamic URL should yield 1 anonymous uncertain edge, got %d", uncertain)
	}
}

func TestOutboundIgnoresNonHTTPReceiver(t *testing.T) {
	// mypkg.Get(...) must not become an edge; nor should calls in files that
	// never import net/http.
	deps := outbound(t, `package main
		import "net/http"
		func x() { cache.Get("key") ; storage.Post("x", nil, nil) }`)
	if len(deps) != 0 {
		t.Errorf("expected no deps, got %+v", deps)
	}
}

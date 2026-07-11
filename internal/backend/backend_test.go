package backend

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/pipeline"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New([]string{"good-key"}, store).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// graph builds a canonical service JSON body (what the extractor submits).
func graph(t *testing.T, mut func(*model.Service)) []byte {
	t.Helper()
	s := model.NewService("orders", "orders", "repo")
	s.Endpoints = []model.Endpoint{{Method: "GET", Path: "/orders/{id}",
		Protocol: model.ProtoREST, Detection: model.DetectAnnotation, Confidence: model.Confirmed}}
	if mut != nil {
		mut(s)
	}
	b, err := pipeline.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ingest(t *testing.T, srv *httptest.Server, key, branch string, body []byte) (*IngestResponse, int) {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/v1/ingest", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	if branch != "" {
		req.Header.Set("X-EKG-Branch", branch)
		req.Header.Set("X-EKG-Default-Branch", "main")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out IngestResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return &out, resp.StatusCode
}

func TestValidateKeys(t *testing.T) {
	srv := newTestServer(t)
	for key, want := range map[string]int{"good-key": 200, "bad-key": 401, "": 401} {
		req, _ := http.NewRequest("POST", srv.URL+"/v1/auth/validate", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("key %q -> %d, want %d", key, resp.StatusCode, want)
		}
	}
}

// TestBaselineLifecycle drives the whole feature: first main scan creates the
// baseline; identical rescan is the fast path; a PR scan diffs WITHOUT
// touching the baseline; the merged main scan becomes the new baseline.
func TestBaselineLifecycle(t *testing.T) {
	srv := newTestServer(t)
	base := graph(t, nil)

	// 1. first scan on main -> baseline created, first_scan
	r, code := ingest(t, srv, "good-key", "main", base)
	if code != 200 || !r.FirstScan || !r.BaselineUpdated || r.Unchanged {
		t.Fatalf("first scan = %+v (code %d)", r, code)
	}
	if r.Markdown == "" || !strings.Contains(r.Markdown, "GET /orders/{id}") {
		t.Errorf("first-scan markdown should list the endpoints:\n%s", r.Markdown)
	}

	// 2. identical rescan on main -> unchanged fast path, no markdown
	r, _ = ingest(t, srv, "good-key", "main", base)
	if !r.Unchanged || r.Markdown != "" || r.BaselineUpdated {
		t.Fatalf("identical rescan = %+v", r)
	}

	// 3. PR scan adds a dependency -> diff + markdown, baseline NOT updated
	prBody := graph(t, func(s *model.Service) {
		s.OutboundDependencies = append(s.OutboundDependencies, model.Dependency{
			TargetName: "payment-service", Protocol: model.ProtoREST,
			Detection: model.DetectFeign, Confidence: model.Confirmed, Resolved: true})
	})
	r, _ = ingest(t, srv, "good-key", "feature/add-payment", prBody)
	if r.BaselineUpdated || r.Unchanged || r.FirstScan {
		t.Fatalf("PR scan flags = %+v", r)
	}
	if r.Diff == nil || r.Diff.Summary.Added != 1 {
		t.Fatalf("PR diff = %+v, want 1 added", r.Diff)
	}
	if !strings.Contains(r.Markdown, "payment-service") {
		t.Errorf("PR markdown:\n%s", r.Markdown)
	}

	// 4. the same PR body rescanned -> baseline still the OLD one, same diff
	r, _ = ingest(t, srv, "good-key", "feature/add-payment", prBody)
	if r.Diff == nil || r.Diff.Summary.Added != 1 {
		t.Fatalf("PR rescan should still diff against the old baseline: %+v", r)
	}

	// 5. merged: main scan with the new body -> diff reported + baseline swap
	r, _ = ingest(t, srv, "good-key", "main", prBody)
	if !r.BaselineUpdated || r.Diff == nil || r.Diff.Summary.Added != 1 {
		t.Fatalf("main scan after merge = %+v", r)
	}

	// 6. the PR body is now the baseline: rescan -> unchanged
	r, _ = ingest(t, srv, "good-key", "main", prBody)
	if !r.Unchanged {
		t.Fatalf("post-swap rescan = %+v", r)
	}
}

func TestIngestRejectsBadKeyAndBody(t *testing.T) {
	srv := newTestServer(t)
	if _, code := ingest(t, srv, "wrong", "main", graph(t, nil)); code != 401 {
		t.Errorf("bad key -> %d, want 401", code)
	}
	if _, code := ingest(t, srv, "good-key", "main", []byte("not json")); code != 400 {
		t.Errorf("bad body -> %d, want 400", code)
	}
	if _, code := ingest(t, srv, "good-key", "main", []byte(`{"service_name":"x"}`)); code != 400 {
		t.Errorf("missing service_id -> %d, want 400", code)
	}
}

func TestStorePathTraversalSafe(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)
	if err := store.Save("../../evil", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	p := store.path("../../evil")
	if !strings.HasPrefix(p, dir) {
		t.Errorf("path escaped the data dir: %s", p)
	}
	if _, found, _ := store.Load("../../evil"); !found {
		t.Error("sanitized id should round-trip")
	}
}

// TestNameToServiceMapping: an added target resolves to a KNOWN service (by
// name or by URL host, k8s style) or is reported as external — both per spec.
func TestNameToServiceMapping(t *testing.T) {
	srv := newTestServer(t)

	// register the fleet: payment-service scans its main branch first
	payment, _ := json.Marshal(map[string]any{"service_id": "payment-service", "service_name": "payment-service",
		"endpoints": []any{}, "outbound_dependencies": []any{}, "kafka_producers": []any{}, "kafka_consumers": []any{},
		"databases_used": []any{}, "config_dependencies": []any{}, "repository": ""})
	if r, code := ingest(t, srv, "good-key", "main", payment); code != 200 || !r.BaselineUpdated {
		t.Fatalf("payment baseline: %+v (%d)", r, code)
	}

	// orders baseline (so the next scan is not first_scan)
	ingest(t, srv, "good-key", "main", graph(t, nil))

	// PR adds three deps: known by name, known by URL host, external
	pr := graph(t, func(s *model.Service) {
		s.OutboundDependencies = append(s.OutboundDependencies,
			model.Dependency{TargetName: "payment-service", Protocol: model.ProtoREST,
				Detection: model.DetectFeign, Confidence: model.Confirmed, Resolved: true},
			model.Dependency{TargetName: "http://payment-service.prod.svc:8080/pay", URL: "http://payment-service.prod.svc:8080/pay",
				Protocol: model.ProtoREST, Detection: model.DetectRestTemplate, Confidence: model.Confirmed, Resolved: true},
			model.Dependency{TargetName: "https://api.stripe.com/v1", URL: "https://api.stripe.com/v1",
				Protocol: model.ProtoREST, Detection: model.DetectWebClient, Confidence: model.Confirmed, Resolved: true})
	})
	r, _ := ingest(t, srv, "good-key", "feature/pay", pr)
	if r.Diff == nil || len(r.Diff.TargetResolutions) != 3 {
		t.Fatalf("resolutions = %+v", r.Diff)
	}
	want := map[string]string{
		"payment-service|feign":                                 "payment-service",
		"http://payment-service.prod.svc:8080/pay|resttemplate": "payment-service",
		"https://api.stripe.com/v1|webclient":                   "external",
	}
	for k, w := range want {
		if got := r.Diff.TargetResolutions[k]; got != w {
			t.Errorf("resolution[%q] = %q, want %q", k, got, w)
		}
	}
	if !strings.Contains(r.Markdown, "known service **payment-service**") {
		t.Errorf("markdown should mark the known service:\n%s", r.Markdown)
	}
	if !strings.Contains(r.Markdown, "· external") {
		t.Errorf("markdown should mark the external target:\n%s", r.Markdown)
	}
}

// TestRegistrySurvivesRestart: the registry is rebuilt from stored baselines.
func TestRegistrySurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	store1, _ := NewStore(dir)
	srv1 := httptest.NewServer(New([]string{"k"}, store1).Handler())
	payment, _ := json.Marshal(map[string]any{"service_id": "payment-service", "service_name": "payments",
		"endpoints": []any{}, "outbound_dependencies": []any{}, "kafka_producers": []any{}, "kafka_consumers": []any{},
		"databases_used": []any{}, "config_dependencies": []any{}, "repository": ""})
	ingest(t, srv1, "k", "main", payment)
	srv1.Close()

	// new process over the same data dir
	store2, _ := NewStore(dir)
	srv2 := New([]string{"k"}, store2)
	dep := model.Dependency{TargetName: "payments", Detection: model.DetectFeign}
	if got := srv2.resolveTarget(dep); got != "payment-service" {
		t.Errorf("after restart resolveTarget = %q, want payment-service (via service_name)", got)
	}
}

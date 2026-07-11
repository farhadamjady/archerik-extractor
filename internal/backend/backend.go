// Package backend is the MVP control-plane server (ekgd): API-key validation
// and result ingestion with per-commit architecture diffing — the server side
// of the "PR impact comment" feature.
//
// Design (per the feature spec):
//   - The extractor stays stateless; the body of /v1/ingest is EXACTLY the
//     extractor's byte-stable Service JSON. Commit metadata travels in headers
//     (X-EKG-Branch, X-EKG-Sha, X-EKG-PR, X-EKG-Default-Branch), so the
//     byte-identical fast path is a plain bytes comparison with the baseline.
//   - One baseline per service (the default branch). A default-branch scan
//     updates the baseline; a PR scan only diffs against it.
//   - The response carries the diff object AND the rendered markdown; the
//     customer's CI posts the comment (the server never touches GitHub).
package backend

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/farhadamjady/service-discovery/internal/graphdiff"
	"github.com/farhadamjady/service-discovery/internal/model"
)

// Server holds the MVP state: the accepted API keys, the baseline store, and
// the fleet-name registry (normalized service name/id -> service_id) used to
// resolve outbound targets to known services.
type Server struct {
	keys  map[string]bool
	store *Store

	mu    sync.RWMutex
	names map[string]string
}

// New builds a server from a comma-separated key list and a data directory.
// The name registry starts from the stored baselines: every service that has
// ever scanned its default branch is "known".
func New(keys []string, store *Store) *Server {
	km := map[string]bool{}
	for _, k := range keys {
		if k = strings.TrimSpace(k); k != "" {
			km[k] = true
		}
	}
	s := &Server{keys: km, store: store, names: map[string]string{}}
	if pairs, err := store.List(); err == nil {
		for _, p := range pairs {
			s.registerName(p[0], p[1])
		}
	}
	return s
}

func (s *Server) registerName(id, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.names[strings.ToLower(id)] = id
	if name != "" {
		s.names[strings.ToLower(name)] = id
	}
}

// resolveTarget maps an outbound dependency to a known service id or
// "external". Matching (minimal, per spec): the raw target name, or the URL's
// hostname / its first dot-label (k8s style: payment-service.ns.svc -> payment-service).
func (s *Server) resolveTarget(dep model.Dependency) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, cand := range targetCandidates(dep) {
		if id, ok := s.names[cand]; ok {
			return id
		}
	}
	return "external"
}

func targetCandidates(dep model.Dependency) []string {
	var out []string
	if dep.TargetName != "" && !strings.Contains(dep.TargetName, "://") {
		out = append(out, strings.ToLower(dep.TargetName))
	}
	for _, raw := range []string{dep.URL, dep.TargetName} {
		if !strings.Contains(raw, "://") {
			continue
		}
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			host := strings.ToLower(u.Hostname())
			out = append(out, host)
			if i := strings.IndexByte(host, '.'); i > 0 {
				out = append(out, host[:i]) // first label: k8s service DNS
			}
		}
	}
	return out
}

// annotateTargets fills diff.TargetResolutions for every outbound entry.
func (s *Server) annotateTargets(d *graphdiff.Diff) {
	add := func(dep model.Dependency) {
		if d.TargetResolutions == nil {
			d.TargetResolutions = map[string]string{}
		}
		d.TargetResolutions[model.DependencyKey(dep)] = s.resolveTarget(dep)
	}
	for _, dep := range d.Outbound.Added {
		add(dep)
	}
	for _, dep := range d.Outbound.Removed {
		add(dep)
	}
	for _, ch := range d.Outbound.Changed {
		add(ch.After)
	}
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/validate", s.handleValidate)
	mux.HandleFunc("POST /v1/ingest", s.handleIngest)
	return mux
}

// IngestResponse is what /v1/ingest returns. CI reads .markdown to post the
// PR comment (empty = nothing to post).
type IngestResponse struct {
	ServiceID       string          `json:"service_id"`
	Unchanged       bool            `json:"unchanged"`        // byte-identical with the baseline
	FirstScan       bool            `json:"first_scan"`       // no baseline existed yet
	BaselineUpdated bool            `json:"baseline_updated"` // default-branch scan stored
	Diff            *graphdiff.Diff `json:"diff,omitempty"`
	Markdown        string          `json:"markdown"`
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]any{
		"plan":            "mvp",
		"quota_remaining": 1_000_000,
		"expires_at":      time.Now().AddDate(1, 0, 0).UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) { // the robust gate: the key is re-validated here
		http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
		return
	}
	body, err := readBody(r)
	if err != nil {
		http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
		return
	}
	var head model.Service
	if err := json.Unmarshal(body, &head); err != nil || head.ServiceID == "" {
		http.Error(w, `{"error":"body is not a service graph"}`, http.StatusBadRequest)
		return
	}

	branch := r.Header.Get("X-EKG-Branch")
	defaultBranch := r.Header.Get("X-EKG-Default-Branch")
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	isDefault := branch == "" || branch == defaultBranch // no branch info = treat as baseline

	resp := IngestResponse{ServiceID: head.ServiceID}

	baseBytes, found, err := s.store.Load(head.ServiceID)
	if err != nil {
		http.Error(w, `{"error":"storage"}`, http.StatusInternalServerError)
		return
	}

	switch {
	case found && bytes.Equal(baseBytes, body):
		resp.Unchanged = true // fast path: nothing changed, nothing to render

	case !found:
		resp.FirstScan = true
		// First contact with this service: diff against an empty graph — the
		// renderer's per-section cap keeps the comment sane.
		empty := model.NewService(head.ServiceID, head.ServiceName, head.Repository)
		d := graphdiff.Compute(empty, &head)
		s.annotateTargets(d)
		resp.Diff, resp.Markdown = d, graphdiff.Markdown(d)

	default:
		var base model.Service
		if err := json.Unmarshal(baseBytes, &base); err != nil {
			http.Error(w, `{"error":"corrupt baseline"}`, http.StatusInternalServerError)
			return
		}
		d := graphdiff.Compute(&base, &head)
		s.annotateTargets(d)
		resp.Diff, resp.Markdown = d, graphdiff.Markdown(d)
	}

	// Baseline rule: only default-branch scans become the stored state.
	if isDefault && !resp.Unchanged {
		if err := s.store.Save(head.ServiceID, body); err != nil {
			http.Error(w, `{"error":"storage"}`, http.StatusInternalServerError)
			return
		}
		resp.BaselineUpdated = true
		s.registerName(head.ServiceID, head.ServiceName)
	}

	writeJSON(w, resp)
}

func (s *Server) authorized(r *http.Request) bool {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return key != "" && s.keys[key]
}

func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	var buf bytes.Buffer
	_, err := buf.ReadFrom(http.MaxBytesReader(nil, r.Body, 32<<20)) // 32 MB cap
	return buf.Bytes(), err
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

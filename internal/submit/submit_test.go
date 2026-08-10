package submit

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/exitcode"
)

func TestSubmitSuccess(t *testing.T) {
	var gotAuth, gotPath, gotType string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	body := []byte(`{"service_id":"x"}`)
	if _, err := Submit(context.Background(), srv.URL, "secret-key", body, Meta{Sha: "abc", Branch: "feature/x", PR: "42"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotPath != ingestPath || gotType != "application/json" {
		t.Errorf("path/type = %q/%q", gotPath, gotType)
	}
	if string(gotBody) != string(body) {
		t.Errorf("body = %s, want %s", gotBody, body)
	}
}

func TestSubmitRejected(t *testing.T) {
	// The backend re-validates the key and rejects -> submit error (exit 20).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := Submit(context.Background(), srv.URL, "k", []byte("{}"), Meta{})
	if exitcode.Of(err) != int(exitcode.Submit) {
		t.Errorf("rejected exit = %d, want %d", exitcode.Of(err), exitcode.Submit)
	}
}

func TestSubmitUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, err := Submit(context.Background(), url, "k", []byte("{}"), Meta{})
	if exitcode.Of(err) != int(exitcode.Submit) {
		t.Errorf("unreachable exit = %d, want %d", exitcode.Of(err), exitcode.Submit)
	}
}

// TestSubmitMetaHeaders: commit metadata travels as X-EKG-* headers and
// the ingest response body comes back to the caller.
func TestSubmitMetaHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"markdown":"### impact"}`))
	}))
	defer srv.Close()

	resp, err := Submit(context.Background(), srv.URL, "k", []byte("{}"),
		Meta{Sha: "abc123", Branch: "feature/x", PR: "42", DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	for h, want := range map[string]string{
		"X-Ekg-Sha": "abc123", "X-Ekg-Branch": "feature/x",
		"X-Ekg-Pr": "42", "X-Ekg-Default-Branch": "main",
	} {
		if got.Get(h) != want {
			t.Errorf("header %s = %q, want %q", h, got.Get(h), want)
		}
	}
	if string(resp) != `{"markdown":"### impact"}` {
		t.Errorf("response body = %s", resp)
	}
}

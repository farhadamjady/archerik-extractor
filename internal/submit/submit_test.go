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
	if err := Submit(context.Background(), srv.URL, "secret-key", body); err != nil {
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

	err := Submit(context.Background(), srv.URL, "k", []byte("{}"))
	if exitcode.Of(err) != int(exitcode.Submit) {
		t.Errorf("rejected exit = %d, want %d", exitcode.Of(err), exitcode.Submit)
	}
}

func TestSubmitUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	err := Submit(context.Background(), url, "k", []byte("{}"))
	if exitcode.Of(err) != int(exitcode.Submit) {
		t.Errorf("unreachable exit = %d, want %d", exitcode.Of(err), exitcode.Submit)
	}
}

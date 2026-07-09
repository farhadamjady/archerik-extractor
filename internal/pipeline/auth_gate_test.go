package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/exitcode"
)

// TestAuthGateAbortsBeforeScan: a rejecting validation server stops the run at
// the auth phase (phase 0), so detection never runs — the run errors with the
// auth exit code, not a detection/scan error.
func TestAuthGateAbortsBeforeScan(t *testing.T) {
	root := springRepo(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := Run(context.Background(), Options{Root: root, APIKey: "bad", APIURL: srv.URL})
	if err == nil {
		t.Fatal("expected auth failure")
	}
	if code := exitcode.Of(err); code != int(exitcode.AuthInvalid) {
		t.Errorf("exit = %d, want %d (401)", code, exitcode.AuthInvalid)
	}
}

// TestAuthGateSuccessProceeds: a 200 validation server lets the scan run.
func TestAuthGateSuccessProceeds(t *testing.T) {
	root := springRepo(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"plan":"pro"}`))
	}))
	defer srv.Close()

	// --dry-run so submit is skipped; auth still runs against the server.
	svc, err := Run(context.Background(), Options{Root: root, APIKey: "good", APIURL: srv.URL, DryRun: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if svc == nil {
		t.Fatal("expected a service graph after successful auth")
	}
}

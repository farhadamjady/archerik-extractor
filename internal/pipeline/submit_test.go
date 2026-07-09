package pipeline

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/exitcode"
)

// backend is a fake control plane handling both the validate and ingest paths.
func backend(t *testing.T, ingestStatus int, gotBody *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/validate":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"plan":"pro"}`))
		case "/v1/ingest":
			if gotBody != nil {
				*gotBody, _ = io.ReadAll(r.Body)
			}
			w.WriteHeader(ingestStatus)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// TestFullRunSubmits: a non-dry-run validates, scans, and submits the exact
// marshaled graph.
func TestFullRunSubmits(t *testing.T) {
	root := springRepo(t)
	var got []byte
	srv := backend(t, http.StatusAccepted, &got)
	defer srv.Close()

	svc, err := Run(context.Background(), Options{Root: root, APIKey: "k", APIURL: srv.URL})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want, _ := Marshal(svc)
	if !bytes.Equal(got, want) {
		t.Errorf("submitted body mismatch:\n got: %s want: %s", got, want)
	}
}

// TestSubmitRejectionFailsRun: an ingest rejection surfaces as exit 20.
func TestSubmitRejectionFailsRun(t *testing.T) {
	root := springRepo(t)
	srv := backend(t, http.StatusForbidden, nil)
	defer srv.Close()

	_, err := Run(context.Background(), Options{Root: root, APIKey: "k", APIURL: srv.URL})
	if exitcode.Of(err) != int(exitcode.Submit) {
		t.Errorf("exit = %d, want %d (submit)", exitcode.Of(err), exitcode.Submit)
	}
}

// TestDryRunSkipsSubmit: --dry-run validates but never hits ingest.
func TestDryRunSkipsSubmit(t *testing.T) {
	root := springRepo(t)
	var got []byte
	srv := backend(t, http.StatusAccepted, &got)
	defer srv.Close()

	if _, err := Run(context.Background(), Options{Root: root, APIKey: "k", APIURL: srv.URL, DryRun: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != nil {
		t.Error("--dry-run must not submit")
	}
}

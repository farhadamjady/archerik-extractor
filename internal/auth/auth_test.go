package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/exitcode"
)

func TestValidateMissingKey(t *testing.T) {
	_, err := Validate(context.Background(), "", "https://api.example.com")
	if exitcode.Of(err) != int(exitcode.AuthMissingKey) {
		t.Errorf("empty key exit = %d, want %d", exitcode.Of(err), exitcode.AuthMissingKey)
	}
}

func TestValidateNoURLPresenceOnly(t *testing.T) {
	// No backend configured: any non-empty key is accepted (dev).
	ent, err := Validate(context.Background(), "some-key", "")
	if err != nil || ent == nil {
		t.Fatalf("presence-only = (%+v, %v), want ok", ent, err)
	}
}

func TestValidateStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		want   exitcode.Code
	}{
		{http.StatusUnauthorized, exitcode.AuthInvalid},
		{http.StatusForbidden, exitcode.AuthNotEntitled},
		{http.StatusTooManyRequests, exitcode.AuthQuota},
		{http.StatusInternalServerError, exitcode.AuthUnreachable}, // 5xx -> fail-closed
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
		}))
		_, err := Validate(context.Background(), "k", srv.URL)
		srv.Close()
		if got := exitcode.Of(err); got != int(c.want) {
			t.Errorf("status %d -> exit %d, want %d", c.status, got, c.want)
		}
	}
}

func TestValidateSuccess(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"plan":"pro","quota_remaining":42}`))
	}))
	defer srv.Close()

	ent, err := Validate(context.Background(), "secret-key", srv.URL)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if ent.Plan != "pro" || ent.QuotaRemaining != 42 {
		t.Errorf("entitlement = %+v", ent)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("auth header = %q, want Bearer secret-key", gotAuth)
	}
	if gotPath != validatePath {
		t.Errorf("path = %q, want %q", gotPath, validatePath)
	}
}

func TestValidateUnreachableFailsClosed(t *testing.T) {
	// Point at a server that is immediately closed -> connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, err := Validate(context.Background(), "k", url)
	if exitcode.Of(err) != int(exitcode.AuthUnreachable) {
		t.Errorf("unreachable exit = %d, want %d (fail-closed)", exitcode.Of(err), exitcode.AuthUnreachable)
	}
}

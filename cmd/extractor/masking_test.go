package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const secret = "sk-live-DO-NOT-LEAK-3f9a2b"

// runCapture runs the CLI and returns combined stdout+stderr.
func runCapture(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	run(args, &stdout, &stderr)
	return stdout.String() + stderr.String()
}

// TestKeyNeverLeaksAcrossPaths audits that the API key value never appears in
// output — on success, auth rejection, or submit rejection.
func TestKeyNeverLeaksAcrossPaths(t *testing.T) {
	root := springRepo(t)

	reject := func(path string, status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == path {
				w.WriteHeader(status)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"plan":"pro"}`))
		}))
	}

	authFail := reject("/v1/auth/validate", http.StatusUnauthorized)
	defer authFail.Close()
	submitFail := reject("/v1/ingest", http.StatusForbidden)
	defer submitFail.Close()

	cases := map[string]string{
		"success dry-run": strings.Join([]string{"--root", root, "--api-key", secret, "--dry-run"}, " "),
		"auth rejected":   strings.Join([]string{"--root", root, "--api-key", secret, "--api-url", authFail.URL}, " "),
		"submit rejected": strings.Join([]string{"--root", root, "--api-key", secret, "--api-url", submitFail.URL}, " "),
	}
	for name, argline := range cases {
		t.Run(name, func(t *testing.T) {
			out := runCapture(t, strings.Fields(argline)...)
			if strings.Contains(out, secret) {
				t.Errorf("API key leaked in output:\n%s", out)
			}
		})
	}
}

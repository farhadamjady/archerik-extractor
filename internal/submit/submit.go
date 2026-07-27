// Package submit sends the finished graph to the ingest API. The key travels
// with the submission, where the backend RE-VALIDATES it — this is the robust
// gate (a local startup check is bypassable). A rejection or network failure is
// a submit error (exit 20).
package submit

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/farhadamjady/service-discovery/internal/exitcode"
)

// ingestPath is the submission endpoint on the backend base URL for a
// service-mode scan's *model.Service graph.
const ingestPath = "/v1/ingest"

// identityIngestPath is the submission endpoint for a deploy-repo-mode scan's
// *model.IdentityMap, and for the .ekg-identity.json fallback declaration
// submitted alongside a service-mode scan. A distinct path (not a body
// discriminator) since the backend's /v1/ingest handler is shaped
// specifically for diffing a Service graph against a stored baseline —
// conflating the two payload shapes on one path would blur that contract.
// The backend tells identity SOURCES (helm/kustomize/k8s-raw/self-declared)
// apart via IdentityEntry.Source, not the URL.
const identityIngestPath = "/v1/ingest/identity-map"

var client = &http.Client{Timeout: 30 * time.Second}

// Meta is the commit context of a scan, sent as headers so the request BODY
// stays the pure byte-stable Service JSON (the backend's fast path compares it
// directly against the stored baseline).
type Meta struct {
	Sha           string // commit being scanned
	Branch        string // branch of the scan (PR head branch on PRs)
	PR            string // pull-request number, "" outside PRs
	DefaultBranch string // baseline branch; backend defaults to "main"
}

// Submit POSTs the marshaled service graph to {baseURL}/v1/ingest with the key
// and commit metadata, and returns the response body (the ingest response
// carries the architecture diff + rendered PR comment). Any non-2xx response
// (including the backend re-validating and rejecting the key) or network
// failure is exit 20.
func Submit(ctx context.Context, baseURL, key string, body []byte, meta Meta) ([]byte, error) {
	return post(ctx, strings.TrimRight(baseURL, "/")+ingestPath, key, body, meta)
}

// SubmitIdentityMap POSTs a marshaled deploy-repo-mode (or self-declared)
// identity map to {baseURL}/v1/ingest/identity-map. Same auth, headers, and
// error handling as Submit — only the endpoint and payload shape differ.
func SubmitIdentityMap(ctx context.Context, baseURL, key string, body []byte, meta Meta) ([]byte, error) {
	return post(ctx, strings.TrimRight(baseURL, "/")+identityIngestPath, key, body, meta)
}

// post is the shared HTTP mechanics behind Submit and SubmitIdentityMap.
func post(ctx context.Context, url, key string, body []byte, meta Meta) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Submit, "build submit request", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	setIf(req, "X-EKG-Sha", meta.Sha)
	setIf(req, "X-EKG-Branch", meta.Branch)
	setIf(req, "X-EKG-PR", meta.PR)
	setIf(req, "X-EKG-Default-Branch", meta.DefaultBranch)

	resp, err := client.Do(req)
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Submit, "submit to ingest API failed", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return respBody, nil
	}
	return nil, exitcode.Errorf(exitcode.Submit, "ingest rejected: status %d", resp.StatusCode)
}

func setIf(req *http.Request, header, value string) {
	if value != "" {
		req.Header.Set(header, value)
	}
}

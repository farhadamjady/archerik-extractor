// Package submit sends the finished graph to the ingest API. The key travels
// with the submission, where the backend RE-VALIDATES it — this is the robust
// gate (a local startup check is bypassable). A rejection or network failure is
// a submit error (exit 20).
package submit

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/farhadamjady/service-discovery/internal/exitcode"
)

// ingestPath is the submission endpoint on the backend base URL.
const ingestPath = "/v1/ingest"

var client = &http.Client{Timeout: 30 * time.Second}

// Submit POSTs the marshaled service graph to {baseURL}/v1/ingest with the key.
// Any non-2xx response (including the backend re-validating and rejecting the
// key) or network failure is exit 20.
func Submit(ctx context.Context, baseURL, key string, body []byte) error {
	url := strings.TrimRight(baseURL, "/") + ingestPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return exitcode.Wrap(exitcode.Submit, "build submit request", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return exitcode.Wrap(exitcode.Submit, "submit to ingest API failed", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return exitcode.Errorf(exitcode.Submit, "ingest rejected: status %d", resp.StatusCode)
}

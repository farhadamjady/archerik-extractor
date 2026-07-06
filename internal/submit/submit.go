// Package submit sends the finished graph to the ingest API. The key travels
// with the submission, where the backend re-validates it — the robust gate (a
// local startup check is bypassable). This is a stub; the authenticated POST and
// re-validation error handling (exit 20) land in PR 24.
package submit

import "context"

// Submit POSTs the marshaled service graph to url with the key. Stub: no-op.
// PR 24 makes this an authenticated request and maps failures to exit code 20.
func Submit(ctx context.Context, url, key string, body []byte) error {
	return nil
}

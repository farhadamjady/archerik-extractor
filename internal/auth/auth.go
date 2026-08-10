// Package auth is the startup entitlement gate for runs that talk to a
// control-plane backend.
//
// Extraction itself is free and offline: with no backend URL configured the
// gate is a no-op and no key is needed. A run that targets a backend
// (--api-url) resolves a per-user API key and validates it BEFORE any
// scanning, fail-closed, so a rejected key costs nothing. That startup check
// is fail-fast UX only — it is bypassable in a local build, and the real
// enforcement is the server-side re-validation at submit.
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/farhadamjady/service-discovery/internal/exitcode"
)

// validatePath is the validation endpoint on the backend base URL.
const validatePath = "/v1/auth/validate"

var client = &http.Client{Timeout: 10 * time.Second}

// Entitlement is what a successful validation returns.
type Entitlement struct {
	Plan           string    `json:"plan"`
	QuotaRemaining int       `json:"quota_remaining"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// Validate gates a run before anything is scanned.
//
// No backend URL: local extraction, no key required, nothing sent anywhere.
// With a URL: an empty key fails fast (exit 10) and a present key is checked
// against the validation API, FAIL-CLOSED — an unreachable server refuses the
// run rather than proceeding to scan and submit.
func Validate(ctx context.Context, key, baseURL string) (*Entitlement, error) {
	if strings.TrimSpace(baseURL) == "" {
		return &Entitlement{Plan: "local"}, nil
	}
	if strings.TrimSpace(key) == "" {
		return nil, exitcode.Errorf(exitcode.AuthMissingKey,
			"no API key for --api-url: set --api-key, the EKG_API_KEY env var, or a config file")
	}
	return validateHTTP(ctx, key, strings.TrimRight(baseURL, "/")+validatePath)
}

func validateHTTP(ctx context.Context, key, url string) (*Entitlement, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, exitcode.Wrap(exitcode.AuthUnreachable, "build validation request", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := client.Do(req)
	if err != nil {
		// Network failure / timeout — fail-closed (accepted outage risk).
		return nil, exitcode.Wrap(exitcode.AuthUnreachable, "validation server unreachable", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var ent Entitlement
		if err := json.NewDecoder(resp.Body).Decode(&ent); err != nil {
			return nil, exitcode.Wrap(exitcode.AuthUnreachable, "decode entitlement", err)
		}
		return &ent, nil
	case http.StatusUnauthorized:
		return nil, exitcode.Errorf(exitcode.AuthInvalid, "API key invalid or expired")
	case http.StatusForbidden:
		return nil, exitcode.Errorf(exitcode.AuthNotEntitled, "API key not entitled to run the extractor")
	case http.StatusTooManyRequests:
		return nil, exitcode.Errorf(exitcode.AuthQuota, "quota exceeded for this API key")
	default:
		// 5xx and anything unexpected: fail-closed.
		return nil, exitcode.Errorf(exitcode.AuthUnreachable,
			"validation failed: unexpected status %d", resp.StatusCode)
	}
}

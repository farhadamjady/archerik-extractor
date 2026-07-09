// Package auth is the startup entitlement gate. The tool is paid: a run resolves
// a per-user API key and validates it BEFORE any scanning, fail-closed. The
// robust gate is server-side at submit (a local check is bypassable); this is
// the fail-fast UX + soft deterrent.
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

// Validate checks the key before scanning. An empty key fails fast (exit 10). If
// no backend URL is configured the gate is presence-only (dev; the robust check
// is server-side at submit). With a URL, it calls the validation API and is
// FAIL-CLOSED: an unreachable server refuses the run rather than proceeding.
func Validate(ctx context.Context, key, baseURL string) (*Entitlement, error) {
	if strings.TrimSpace(key) == "" {
		return nil, exitcode.Errorf(exitcode.AuthMissingKey,
			"no API key: set --api-key, the EKG_API_KEY env var, or a config file")
	}
	if strings.TrimSpace(baseURL) == "" {
		return &Entitlement{Plan: "unvalidated"}, nil
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

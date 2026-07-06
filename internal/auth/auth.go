// Package auth is the startup entitlement gate. The tool is paid: a run resolves
// a per-user API key and validates it BEFORE any scanning, fail-closed. This is
// a stub — it only enforces key PRESENCE. The real phone-home validation
// (POST /v1/auth/validate, 401/403/429/unreachable handling) lands in PR 23; the
// robust gate is the server re-validating at submit time regardless.
package auth

import (
	"context"
	"strings"
	"time"

	"github.com/farhadamjady/service-discovery/internal/exitcode"
)

// Entitlement is what a successful validation returns. Populated for real by the
// validation server in PR 23.
type Entitlement struct {
	Plan           string
	QuotaRemaining int
	ExpiresAt      time.Time
}

// Validate checks the key before scanning. Empty key → AuthMissingKey (exit 10),
// fail before any work. A present key is accepted by this stub; PR 23 replaces
// the acceptance with a real validation call.
func Validate(ctx context.Context, key string) (*Entitlement, error) {
	if strings.TrimSpace(key) == "" {
		return nil, exitcode.Errorf(exitcode.AuthMissingKey,
			"no API key: set --api-key, the EKG_API_KEY env var, or a config file")
	}
	return &Entitlement{Plan: "local-stub"}, nil
}

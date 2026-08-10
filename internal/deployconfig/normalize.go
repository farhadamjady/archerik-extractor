// Package deployconfig reads externalized deployment configuration — Helm
// values, rendered Kubernetes manifests, and .env files — into env-var bindings
// that the config resolver can bridge to Spring properties. It is
// framework-neutral: env vars and K8s/Helm are not Spring-specific.
//
// This package parses those sources and normalizes their keys. Wiring the
// bindings into a layered ConfigResolver and emitting overlay candidates is
// the framework provider's job (see spring's deployindexer) — this layer
// stays framework-neutral.
package deployconfig

import (
	"strings"
	"unicode"
)

// NormalizeKey folds a config / env-var / values key to a canonical
// dot.lower form so Spring's relaxed binding unifies the ways the same setting
// is written across layers:
//
//	payment.service.url  PAYMENT_SERVICE_URL  payment-service-url  paymentServiceUrl
//
// all normalize to "payment.service.url". Separators (. _ - / space) and
// camelCase humps (including ACRONYM boundaries, URLPath -> url.path) become
// word boundaries; everything is lowercased.
func NormalizeKey(s string) string {
	var words []string
	var cur []rune
	runes := []rune(s)

	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = nil
		}
	}

	for i, r := range runes {
		switch {
		case r == '.' || r == '_' || r == '-' || r == '/' || r == ' ':
			flush()
		case unicode.IsUpper(r):
			if len(cur) > 0 {
				prev := runes[i-1]
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				// New word when following a lowercase/digit (fooBar), or when an
				// uppercase run ends into a lowercase (URLPath -> URL | Path).
				if unicode.IsLower(prev) || unicode.IsDigit(prev) {
					flush()
				} else if unicode.IsUpper(prev) && nextLower {
					flush()
				}
			}
			cur = append(cur, unicode.ToLower(r))
		default:
			cur = append(cur, unicode.ToLower(r))
		}
	}
	flush()
	return strings.Join(words, ".")
}

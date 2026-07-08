package deployconfig

import "testing"

// TestNormalizeConvergence is the relaxed-binding contract (E4): the four ways a
// setting is written must all fold to one canonical key.
func TestNormalizeConvergence(t *testing.T) {
	forms := []string{
		"payment.service.url",
		"PAYMENT_SERVICE_URL",
		"payment-service-url",
		"paymentServiceUrl",
	}
	const want = "payment.service.url"
	for _, f := range forms {
		if got := NormalizeKey(f); got != want {
			t.Errorf("NormalizeKey(%q) = %q, want %q", f, got, want)
		}
	}
}

func TestNormalizeCases(t *testing.T) {
	cases := map[string]string{
		"URL":                  "url",
		"serviceURL":           "service.url", // acronym after a word
		"URLPath":              "url.path",    // acronym before a word
		"payment.serviceUrl":   "payment.service.url",
		"S3_BUCKET":            "s3.bucket",
		"s3Bucket":             "s3.bucket",
		"SPRING_KAFKA_SERVERS": "spring.kafka.servers",
		"a":                    "a",
		"":                     "",
	}
	for in, want := range cases {
		if got := NormalizeKey(in); got != want {
			t.Errorf("NormalizeKey(%q) = %q, want %q", in, got, want)
		}
	}
}

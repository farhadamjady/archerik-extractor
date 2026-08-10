package deployconfig

import (
	"testing"
)

// parseMap parses a file and returns normalized-key -> value for easy asserting.
func parseMap(t *testing.T, path, src string) map[string]string {
	t.Helper()
	bs, err := Parse(path, []byte(src))
	if err != nil {
		t.Fatalf("Parse(%s): %v", path, err)
	}
	out := map[string]string{}
	for _, b := range bs {
		out[b.Key] = b.Value
	}
	return out
}

func TestParseDotenv(t *testing.T) {
	got := parseMap(t, "config/.env", `
# comment
export PAYMENT_SERVICE_URL=http://payment:8080
ORDERS_TOPIC="orders.v1"
BLANK=
`)
	if got["payment.service.url"] != "http://payment:8080" {
		t.Errorf("payment.service.url = %q", got["payment.service.url"])
	}
	if got["orders.topic"] != "orders.v1" {
		t.Errorf("orders.topic = %q (quotes stripped)", got["orders.topic"])
	}
}

func TestParseValues(t *testing.T) {
	// Helm values keyed by path; relaxed binding bridges serviceUrl -> service.url.
	got := parseMap(t, "chart/values.yaml", `
payment:
  serviceUrl: http://payment
  timeout: 30
replicas: 3
`)
	if got["payment.service.url"] != "http://payment" {
		t.Errorf("payment.service.url = %q", got["payment.service.url"])
	}
	if got["payment.timeout"] != "30" {
		t.Errorf("payment.timeout = %q", got["payment.timeout"])
	}
}

func TestParseConfigMap(t *testing.T) {
	got := parseMap(t, "k8s/configmap.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: cart
data:
  ORDERS_TOPIC: orders.v1
  APP_YML: |
    spring:
      application:
        name: cart
`)
	if got["orders.topic"] != "orders.v1" {
		t.Errorf("orders.topic = %q", got["orders.topic"])
	}
	// The embedded multi-line application.yml blob is skipped, not misparsed.
	if _, ok := got["app.yml"]; ok {
		t.Error("multi-line ConfigMap data (embedded file) should be skipped")
	}
}

func TestParseDeploymentEnv(t *testing.T) {
	got := parseMap(t, "k8s/deployment.yaml", `
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
        - name: app
          env:
            - name: PAYMENT_SERVICE_URL
              value: http://payment:8080
            - name: DB_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: db
                  key: password
`)
	if got["payment.service.url"] != "http://payment:8080" {
		t.Errorf("payment.service.url = %q", got["payment.service.url"])
	}
	// valueFrom has no literal — must not appear as a binding.
	if _, ok := got["db.password"]; ok {
		t.Error("valueFrom env should not produce a literal binding")
	}
}

func TestHelmTemplateSkipped(t *testing.T) {
	bs, err := Parse("chart/templates/deployment.yaml", []byte(`
kind: Deployment
spec:
  template:
    spec:
      containers:
        - env:
            - name: PAYMENT_SERVICE_URL
              value: {{ .Values.payment.serviceUrl }}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(bs) != 0 {
		t.Errorf("Helm template should be skipped by Parse (TraceTemplates handles it), got %d bindings", len(bs))
	}
}

func TestOverlayFromFilename(t *testing.T) {
	bs, err := Parse("chart/values-staging.yaml", []byte("payment:\n  url: http://staging\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 1 || bs[0].Overlay != "staging" {
		t.Errorf("overlay = %+v, want overlay=staging", bs)
	}
}

func TestLayerCandidatesAndDedup(t *testing.T) {
	l := NewLayer()
	l.Add(Binding{Key: "payment.url", Value: "http://base"})
	l.Add(Binding{Key: "payment.url", Value: "http://base"}) // exact dup
	l.Add(Binding{Key: "payment.url", Value: "http://prod", Overlay: "prod"})

	got := l.Get("payment.url")
	if len(got) != 2 {
		t.Fatalf("got %d bindings, want 2 (dedup exact, keep divergent overlay)", len(got))
	}
	if keys := l.Keys(); len(keys) != 1 || keys[0] != "payment.url" {
		t.Errorf("Keys() = %v", keys)
	}
}

// TestParseK8sStrict: repo-root discovery keeps only k8s documents — unrelated
// yaml (skaffold, CI) adds no bindings.
func TestParseK8sStrict(t *testing.T) {
	cm, err := ParseK8s("kubernetes-manifests/config.yaml", []byte(`
apiVersion: v1
kind: ConfigMap
metadata: { name: service-api-config }
data:
  BALANCES_API_ADDR: "balancereader:8080"
`))
	if err != nil || len(cm) != 1 || cm[0].Key != "balances.api.addr" {
		t.Fatalf("configmap = %+v err=%v", cm, err)
	}
	skaffold, err := ParseK8s("skaffold.yaml", []byte("apiVersion: skaffold/v4\nkind: Config\nbuild:\n  artifacts:\n    - image: x\n"))
	if err != nil || len(skaffold) != 0 {
		t.Errorf("skaffold must add nothing, got %+v", skaffold)
	}
}

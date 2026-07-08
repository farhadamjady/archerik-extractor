package deployconfig

import "testing"

// valuesLayer builds a values Layer from one values file for tracing tests.
func valuesLayer(t *testing.T, path, src string) *Layer {
	t.Helper()
	bs, err := Parse(path, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	l := NewLayer()
	for _, b := range bs {
		l.Add(b)
	}
	return l
}

func TestTraceValuesRef(t *testing.T) {
	values := valuesLayer(t, "values.yaml", "payment:\n  serviceUrl: http://payment\n")
	tpl := NamedSource{Path: "templates/deployment.yaml", Src: []byte(`
spec:
  template:
    spec:
      containers:
        - name: app
          env:
            - name: PAYMENT_SERVICE_URL
              value: {{ .Values.payment.serviceUrl | quote }}
            - name: DB_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: db
                  key: password
`)}

	got := TraceTemplates([]NamedSource{tpl}, values)
	if len(got) != 1 {
		t.Fatalf("got %d bindings, want 1: %+v", len(got), got)
	}
	b := got[0]
	// env var name -> relaxed-bound key; value pulled from values.yaml
	if b.Key != "payment.service.url" || b.Value != "http://payment" {
		t.Errorf("binding = %+v, want key=payment.service.url value=http://payment", b)
	}
	// The container `name: app` and the secret-ref env must not pair.
	if b.Raw != "PAYMENT_SERVICE_URL" {
		t.Errorf("raw name = %q, want PAYMENT_SERVICE_URL", b.Raw)
	}
}

func TestTraceOverlayPropagates(t *testing.T) {
	values := NewLayer()
	base, _ := Parse("values.yaml", []byte("host: base-host\n"))
	stg, _ := Parse("values-staging.yaml", []byte("host: staging-host\n"))
	for _, b := range append(base, stg...) {
		values.Add(b)
	}

	tpl := NamedSource{Path: "templates/deploy.yaml", Src: []byte(
		"        - name: SERVICE_HOST\n          value: {{ .Values.host }}\n")}

	got := TraceTemplates([]NamedSource{tpl}, values)
	// base + staging both resolve, so the env var gets two candidate bindings.
	vals := map[string]string{}
	for _, b := range got {
		if b.Key != "service.host" {
			t.Errorf("unexpected key %q", b.Key)
		}
		vals[b.Overlay] = b.Value
	}
	if vals[""] != "base-host" || vals["staging"] != "staging-host" {
		t.Errorf("overlay values = %v, want base-host + staging-host", vals)
	}
}

func TestTraceNonValuesSkipped(t *testing.T) {
	values := NewLayer()
	tpl := NamedSource{Path: "t.yaml", Src: []byte(
		"        - name: FROM_HELPER\n          value: {{ include \"chart.fullname\" . }}\n")}
	if got := TraceTemplates([]NamedSource{tpl}, values); len(got) != 0 {
		t.Errorf("non-.Values expression should not resolve, got %+v", got)
	}
}

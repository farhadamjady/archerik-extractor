package deployrepo

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"gopkg.in/yaml.v3"
)

func doc(t *testing.T, src string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(src), &m); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	return m
}

// testKinds enables Ingress + VirtualService consumption, the default set.
func testKinds() ResolverOptions { return ResolverOptions{Ingress: true, Istio: true} }

func TestExtractEntriesService(t *testing.T) {
	d := doc(t, `
kind: Service
metadata:
  name: pym-service
  namespace: payments
`)
	entries := extractEntries([]k8sDoc{{Path: "service.yaml", Environment: "staging", Doc: d}}, model.SourceK8sRaw, testKinds())
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.ServiceName != "pym-service" || e.Namespace != "payments" || e.Environment != "staging" {
		t.Errorf("entry = %+v", e)
	}
	if e.Confidence != model.IdentityConfirmed || e.Source != model.SourceK8sRaw {
		t.Errorf("confidence/source = %v/%v", e.Confidence, e.Source)
	}
	want := []string{"pym-service", "pym-service.payments", "pym-service.payments.svc", "pym-service.payments.svc.cluster.local"}
	got := model.HostValues(e.Hosts)
	if len(got) != len(want) {
		t.Fatalf("hosts = %v, want %v", got, want)
	}
	for i, h := range want {
		if got[i] != h {
			t.Errorf("hosts[%d] = %q, want %q", i, got[i], h)
		}
		if e.Hosts[i].Kind != model.HostInCluster {
			t.Errorf("hosts[%d] kind = %q, want in-cluster", i, e.Hosts[i].Kind)
		}
	}
}

func TestExtractEntriesServiceNoNamespaceDefaults(t *testing.T) {
	d := doc(t, `
kind: Service
metadata:
  name: pym-service
`)
	entries := extractEntries([]k8sDoc{{Doc: d}}, model.SourceHelm, testKinds())
	if len(entries) != 1 || entries[0].Namespace != "default" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestExtractEntriesIngressFoldsIntoService(t *testing.T) {
	svc := doc(t, `
kind: Service
metadata:
  name: pym-service
  namespace: payments
`)
	ing := doc(t, `
kind: Ingress
metadata:
  name: pym-ingress
  namespace: payments
spec:
  rules:
  - host: payments.example.com
    http:
      paths:
      - backend:
          service:
            name: pym-service
            port:
              number: 80
`)
	entries := extractEntries([]k8sDoc{{Doc: svc}, {Doc: ing}}, model.SourceK8sRaw, testKinds())
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 (Ingress should fold into Service, not stand alone): %+v", len(entries), entries)
	}
	found := false
	for _, h := range entries[0].Hosts {
		if h.Value == "payments.example.com" {
			found = true
			if h.Kind != model.HostExternal {
				t.Errorf("folded ingress host kind = %q, want external", h.Kind)
			}
		}
	}
	if !found {
		t.Errorf("hosts = %v, want to include payments.example.com", model.HostValues(entries[0].Hosts))
	}
	if entries[0].Confidence != model.IdentityConfirmed {
		t.Errorf("confidence = %v, want confirmed (Service was present)", entries[0].Confidence)
	}
}

func TestExtractEntriesIngressLegacyBackendShape(t *testing.T) {
	ing := doc(t, `
kind: Ingress
metadata:
  name: legacy-ingress
  namespace: payments
spec:
  rules:
  - host: legacy.example.com
    http:
      paths:
      - backend:
          serviceName: pym-service
          servicePort: 80
`)
	entries := extractEntries([]k8sDoc{{Doc: ing}}, model.SourceK8sRaw, testKinds())
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 standalone entry: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.ServiceName != "pym-service" || e.Confidence != model.IdentityLikely {
		t.Errorf("entry = %+v, want ServiceName=pym-service Confidence=likely (Service not seen)", e)
	}
}

func TestExtractEntriesVirtualServiceFoldsAndStandalone(t *testing.T) {
	svc := doc(t, `
kind: Service
metadata:
  name: pym-service
  namespace: payments
`)
	vs := doc(t, `
kind: VirtualService
metadata:
  name: pym-vs
  namespace: payments
spec:
  hosts:
  - pym.example.com
  http:
  - route:
    - destination:
        host: pym-service.payments.svc.cluster.local
`)
	entries := extractEntries([]k8sDoc{{Doc: svc}, {Doc: vs}}, model.SourceK8sRaw, testKinds())
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 (VirtualService should fold into Service): %+v", len(entries), entries)
	}
	found := false
	for _, h := range entries[0].Hosts {
		if h.Value == "pym.example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("hosts = %v, want to include pym.example.com", model.HostValues(entries[0].Hosts))
	}

	// Without the Service doc present, the VirtualService's destination
	// should still emit a standalone, likely-confidence entry.
	standalone := extractEntries([]k8sDoc{{Doc: vs}}, model.SourceK8sRaw, testKinds())
	if len(standalone) != 1 || standalone[0].ServiceName != "pym-service" || standalone[0].Confidence != model.IdentityLikely {
		t.Fatalf("standalone entries = %+v", standalone)
	}
}

func TestSplitVirtualServiceHost(t *testing.T) {
	cases := []struct {
		host, wantSvc, wantNS string
	}{
		{"pym-service", "pym-service", ""}, // bare: ns left empty for the caller to default
		{"pym-service.orders", "pym-service", "orders"},
		{"pym-service.orders.svc.cluster.local", "pym-service", "orders"},
	}
	for _, c := range cases {
		svc, ns := splitVirtualServiceHost(c.host)
		if svc != c.wantSvc || ns != c.wantNS {
			t.Errorf("splitVirtualServiceHost(%q) = (%q, %q), want (%q, %q)", c.host, svc, ns, c.wantSvc, c.wantNS)
		}
	}
}

func TestNamespaceConventionFillsUndeclared(t *testing.T) {
	svc := doc(t, "kind: Service\nmetadata:\n  name: pym-service\n") // no namespace declared
	cases := []struct {
		convention, wantNS string
	}{
		{"", "default"},                 // off: unchanged default behavior
		{"service-name", "pym-service"}, // ns == service name
		{"replace:-service:", "pym"},    // pym-service -> pym
	}
	for _, c := range cases {
		opts := ResolverOptions{Ingress: true, Istio: true, NamespaceConvention: c.convention}
		entries := extractEntries([]k8sDoc{{Doc: svc}}, model.SourceK8sRaw, opts)
		if len(entries) != 1 || entries[0].Namespace != c.wantNS {
			t.Errorf("convention %q -> namespace %+v, want %q", c.convention, entries, c.wantNS)
		}
	}
}

func TestNamespaceConventionRespectsDeclared(t *testing.T) {
	svc := doc(t, "kind: Service\nmetadata:\n  name: pym-service\n  namespace: explicit\n")
	opts := ResolverOptions{Ingress: true, Istio: true, NamespaceConvention: "service-name"}
	entries := extractEntries([]k8sDoc{{Doc: svc}}, model.SourceK8sRaw, opts)
	if len(entries) != 1 || entries[0].Namespace != "explicit" {
		t.Errorf("declared namespace must win over convention; got %+v", entries)
	}
}

func TestKindGatingDropsIngress(t *testing.T) {
	svc := doc(t, "kind: Service\nmetadata:\n  name: pym-service\n  namespace: payments\n")
	ing := doc(t, `kind: Ingress
metadata:
  name: ing
  namespace: payments
spec:
  rules:
  - host: pay.example.com
    http:
      paths:
      - backend:
          service:
            name: pym-service
`)
	// Ingress disabled: its external host must not be folded in.
	opts := ResolverOptions{Ingress: false, Istio: false}
	entries := extractEntries([]k8sDoc{{Doc: svc}, {Doc: ing}}, model.SourceK8sRaw, opts)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want 1", entries)
	}
	for _, h := range entries[0].Hosts {
		if h.Value == "pay.example.com" {
			t.Errorf("ingress host leaked despite ingress kind disabled: %+v", model.HostValues(entries[0].Hosts))
		}
	}
}

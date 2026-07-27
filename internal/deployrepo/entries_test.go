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

func TestExtractEntriesService(t *testing.T) {
	d := doc(t, `
kind: Service
metadata:
  name: pym-service
  namespace: payments
`)
	entries := extractEntries([]k8sDoc{{Path: "service.yaml", Environment: "staging", Doc: d}}, model.SourceK8sRaw)
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
	if len(e.Hosts) != len(want) {
		t.Fatalf("hosts = %v, want %v", e.Hosts, want)
	}
	for i, h := range want {
		if e.Hosts[i] != h {
			t.Errorf("hosts[%d] = %q, want %q", i, e.Hosts[i], h)
		}
	}
}

func TestExtractEntriesServiceNoNamespaceDefaults(t *testing.T) {
	d := doc(t, `
kind: Service
metadata:
  name: pym-service
`)
	entries := extractEntries([]k8sDoc{{Doc: d}}, model.SourceHelm)
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
	entries := extractEntries([]k8sDoc{{Doc: svc}, {Doc: ing}}, model.SourceK8sRaw)
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 (Ingress should fold into Service, not stand alone): %+v", len(entries), entries)
	}
	found := false
	for _, h := range entries[0].Hosts {
		if h == "payments.example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("hosts = %v, want to include payments.example.com", entries[0].Hosts)
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
	entries := extractEntries([]k8sDoc{{Doc: ing}}, model.SourceK8sRaw)
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
	entries := extractEntries([]k8sDoc{{Doc: svc}, {Doc: vs}}, model.SourceK8sRaw)
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 (VirtualService should fold into Service): %+v", len(entries), entries)
	}
	found := false
	for _, h := range entries[0].Hosts {
		if h == "pym.example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("hosts = %v, want to include pym.example.com", entries[0].Hosts)
	}

	// Without the Service doc present, the VirtualService's destination
	// should still emit a standalone, likely-confidence entry.
	standalone := extractEntries([]k8sDoc{{Doc: vs}}, model.SourceK8sRaw)
	if len(standalone) != 1 || standalone[0].ServiceName != "pym-service" || standalone[0].Confidence != model.IdentityLikely {
		t.Fatalf("standalone entries = %+v", standalone)
	}
}

func TestSplitVirtualServiceHost(t *testing.T) {
	cases := []struct {
		host, defaultNS, wantSvc, wantNS string
	}{
		{"pym-service", "payments", "pym-service", "payments"},
		{"pym-service.orders", "payments", "pym-service", "orders"},
		{"pym-service.orders.svc.cluster.local", "payments", "pym-service", "orders"},
	}
	for _, c := range cases {
		svc, ns := splitVirtualServiceHost(c.host, c.defaultNS)
		if svc != c.wantSvc || ns != c.wantNS {
			t.Errorf("splitVirtualServiceHost(%q, %q) = (%q, %q), want (%q, %q)", c.host, c.defaultNS, svc, ns, c.wantSvc, c.wantNS)
		}
	}
}

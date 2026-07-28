package deployrepo

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
)

// k8sDoc is one parsed Kubernetes YAML document, tagged with the file it came
// from (or the chart/overlay it was rendered from) and the environment it
// belongs to, if known.
type k8sDoc struct {
	Path        string
	Environment string
	Doc         map[string]any
}

// extractEntries is the kind-dispatch that turns a batch of k8s documents —
// from a raw manifest, a rendered Helm chart, or a rendered Kustomize overlay
// — into IdentityEntry facts. Service documents become confirmed entries.
// Ingress/VirtualService documents fold their externally routed hosts into
// the matching Service entry (by name+namespace+environment) when one is
// present in the same batch; otherwise they emit a standalone entry naming
// the backend service by string, confidence downgraded to likely since its
// existence is unconfirmed within this scan.
//
// Scope: VirtualService handling covers the common case — direct spec.hosts
// and spec.http[].route[].destination.host mapping. It does not resolve
// DestinationRule subsets, traffic mirroring, or weighted-route nuance.
func extractEntries(docs []k8sDoc, source model.IdentitySource, opts ResolverOptions) []model.IdentityEntry {
	byKey := map[string]*model.IdentityEntry{}

	for _, d := range docs {
		if kind(d.Doc) != "Service" {
			continue
		}
		name, ok := digStr(d.Doc, "metadata", "name")
		if !ok || name == "" {
			continue
		}
		declared, _ := digStr(d.Doc, "metadata", "namespace")
		ns := namespaceOrDefault(declared, name, opts)
		byKey[entryKey(name, ns, d.Environment)] = &model.IdentityEntry{
			ServiceName: name,
			Namespace:   ns,
			Environment: d.Environment,
			Source:      source,
			Confidence:  model.IdentityConfirmed,
			Hosts:       serviceHosts(name, ns, source),
		}
	}

	var standalone []*model.IdentityEntry
	for _, d := range docs {
		switch kind(d.Doc) {
		case "Ingress":
			if opts.Ingress {
				foldIngress(d, byKey, &standalone, source, opts)
			}
		case "VirtualService":
			if opts.Istio {
				foldVirtualService(d, byKey, &standalone, source, opts)
			}
		}
	}

	out := make([]model.IdentityEntry, 0, len(byKey)+len(standalone))
	for _, e := range byKey {
		out = append(out, *e)
	}
	for _, e := range standalone {
		out = append(out, *e)
	}
	return out
}

// foldIngress attaches each rule's host to the Service its path routes to,
// handling both the current networking.k8s.io/v1 shape
// (backend.service.name) and the legacy extensions/v1beta1 shape
// (backend.serviceName) — deploy repos in the wild still carry both.
func foldIngress(d k8sDoc, byKey map[string]*model.IdentityEntry, standalone *[]*model.IdentityEntry, source model.IdentitySource, opts ResolverOptions) {
	declaredNS, _ := digStr(d.Doc, "metadata", "namespace")
	targets := map[string][]string{}
	for _, r := range digList(d.Doc, "spec", "rules") {
		rm := asMap(r)
		host, _ := rm["host"].(string)
		for _, p := range digList(rm, "http", "paths") {
			pm := asMap(p)
			svc := ingressBackendServiceName(pm)
			if svc == "" {
				continue
			}
			if _, ok := targets[svc]; !ok {
				targets[svc] = []string{}
			}
			if host != "" {
				targets[svc] = append(targets[svc], host)
			}
		}
	}
	for svc, hosts := range targets {
		ns := namespaceOrDefault(declaredNS, svc, opts)
		attachOrStandalone(svc, ns, d.Environment, hosts, byKey, standalone, source)
	}
}

func ingressBackendServiceName(pathMap map[string]any) string {
	if name, ok := digStr(pathMap, "backend", "service", "name"); ok && name != "" {
		return name
	}
	if name, ok := digStr(pathMap, "backend", "serviceName"); ok && name != "" {
		return name
	}
	return ""
}

// foldVirtualService attaches the VirtualService's external spec.hosts to
// every Service its routes point at. destination.host may be a bare Service
// name (resolved within the VirtualService's own namespace, per Istio's short-
// name resolution), a "name.namespace" pair, or a full
// name.namespace.svc.cluster.local FQDN.
func foldVirtualService(d k8sDoc, byKey map[string]*model.IdentityEntry, standalone *[]*model.IdentityEntry, source model.IdentitySource, opts ResolverOptions) {
	declaredNS, _ := digStr(d.Doc, "metadata", "namespace")
	extHosts := digStrList(d.Doc, "spec", "hosts")

	type target struct{ svc, ns string }
	seen := map[target]bool{}
	for _, hb := range digList(d.Doc, "spec", "http") {
		for _, r := range digList(asMap(hb), "route") {
			destHost, _ := digStr(asMap(r), "destination", "host")
			if destHost == "" {
				continue
			}
			svc, ns := splitVirtualServiceHost(destHost)
			if ns == "" { // bare name: apply the VS's namespace, else default/convention
				ns = namespaceOrDefault(declaredNS, svc, opts)
			}
			seen[target{svc, ns}] = true
		}
	}
	for t := range seen {
		attachOrStandalone(t.svc, t.ns, d.Environment, extHosts, byKey, standalone, source)
	}
}

// splitVirtualServiceHost derives (service name, namespace) from a destination
// host string. A bare name (no dot) yields ns == "" so the caller applies its
// own namespace default/convention; "name.ns" and a full FQDN
// ("name.namespace.svc.cluster.local") both yield the second segment.
func splitVirtualServiceHost(host string) (svc, ns string) {
	parts := strings.Split(host, ".")
	svc = parts[0]
	if len(parts) >= 2 {
		ns = parts[1]
	}
	return svc, ns
}

// attachOrStandalone merges external hosts into an existing (svc, ns, env)
// entry, or — when no Service doc for that key was seen in this batch — emits a
// new standalone entry at Likely confidence: the Ingress/VirtualService names
// the service by string, but its existence is unconfirmed within this scan.
// The routed hostnames are external (opaque) hosts, matched exactly by the
// backend.
func attachOrStandalone(svc, ns, env string, extHosts []string, byKey map[string]*model.IdentityEntry, standalone *[]*model.IdentityEntry, source model.IdentitySource) {
	if len(extHosts) == 0 {
		return
	}
	hosts := make([]model.Host, len(extHosts))
	for i, h := range extHosts {
		hosts[i] = model.Host{Value: h, Kind: model.HostExternal, Resolver: source}
	}
	if e, ok := byKey[entryKey(svc, ns, env)]; ok {
		e.Hosts = append(e.Hosts, hosts...)
		return
	}
	*standalone = append(*standalone, &model.IdentityEntry{
		ServiceName: svc,
		Namespace:   ns,
		Environment: env,
		Source:      source,
		Confidence:  model.IdentityLikely,
		Hosts:       hosts,
	})
}

// serviceHosts is every in-cluster form a Service's name resolves to, each
// tagged in-cluster (the backend matches them after normalizing a caller's
// k8s/mesh FQDN to its bare service name).
func serviceHosts(name, ns string, source model.IdentitySource) []model.Host {
	forms := []string{
		name,
		name + "." + ns,
		name + "." + ns + ".svc",
		name + "." + ns + ".svc.cluster.local",
	}
	out := make([]model.Host, len(forms))
	for i, f := range forms {
		out[i] = model.Host{Value: f, Kind: model.HostInCluster, Resolver: source}
	}
	return out
}

func entryKey(name, ns, env string) string { return name + "\x00" + ns + "\x00" + env }

func kind(doc map[string]any) string {
	k, _ := doc["kind"].(string)
	return k
}

func dig(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		cm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = cm[k]
	}
	return cur
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func asList(v any) []any         { l, _ := v.([]any); return l }

func digStr(m map[string]any, keys ...string) (string, bool) {
	s, ok := dig(m, keys...).(string)
	return s, ok
}

func digList(m map[string]any, keys ...string) []any {
	return asList(dig(m, keys...))
}

func digStrList(m map[string]any, keys ...string) []string {
	items := digList(m, keys...)
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

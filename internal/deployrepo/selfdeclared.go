package deployrepo

import (
	"encoding/json"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/scan"
)

// ekgIdentityDecl is one entry of the .ekg-identity.json fallback file: a
// small, hand-authored declaration of the hosts a service is reachable at, for
// estates with no parseable deploy repo.
type ekgIdentityDecl struct {
	ServiceName string   `json:"service_name"`
	Hosts       []string `json:"hosts"`
	Namespace   string   `json:"namespace"`
	Environment string   `json:"environment"`
}

// selfDeclaredResolver reads .ekg-identity.json at the repo root. Harmless in a
// deploy repo without the file (no entries); the primary use is a service
// repo's scan (see ReadSelfDeclared, called from the CLI's scan-repo path).
type selfDeclaredResolver struct{}

func (selfDeclaredResolver) Name() string { return "self-declared" }

func (selfDeclaredResolver) Resolve(rc ResolveContext) ([]model.IdentityEntry, []RenderError) {
	return parseSelfDeclared(rc.Tree), nil
}

// ReadSelfDeclared reads .ekg-identity.json at root and returns its entries —
// the CLI entry point for the scan-repo side-submission. Absent or malformed
// file yields no entries and no error; it never fails the scan.
func ReadSelfDeclared(root string) []model.IdentityEntry {
	return parseSelfDeclared(scan.NewOSFileTree(root, nil))
}

// parseSelfDeclared reads and parses .ekg-identity.json from tree. Tolerates
// either a bare object or an array of objects (the same tolerant spirit as the
// CLI's dual "="/":" api-key parsing). Each declared host is recorded with the
// self-declared resolver and no Kind — a self-declaration can't reliably say
// whether a host is in-cluster or external, so the backend exact-matches it.
func parseSelfDeclared(tree provider.FileTree) []model.IdentityEntry {
	b, err := tree.Read(".ekg-identity.json")
	if err != nil {
		return nil
	}
	var decls []ekgIdentityDecl
	if err := json.Unmarshal(b, &decls); err != nil {
		var single ekgIdentityDecl
		if err := json.Unmarshal(b, &single); err != nil {
			return nil
		}
		decls = []ekgIdentityDecl{single}
	}
	entries := make([]model.IdentityEntry, 0, len(decls))
	for _, d := range decls {
		if d.ServiceName == "" {
			continue
		}
		hosts := make([]model.Host, 0, len(d.Hosts))
		for _, h := range d.Hosts {
			hosts = append(hosts, model.Host{Value: h, Resolver: model.SourceSelfDeclared})
		}
		entries = append(entries, model.IdentityEntry{
			ServiceName: d.ServiceName,
			Hosts:       hosts,
			Namespace:   d.Namespace,
			Environment: d.Environment,
			Source:      model.SourceSelfDeclared,
			Confidence:  model.IdentityLikely,
		})
	}
	return entries
}

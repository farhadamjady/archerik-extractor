package spring

import (
	"bytes"
	"sort"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/schema/contract"
)

// IngestSpecs implements provider.SpecIngester: when the
// build GENERATES controllers from an OpenAPI spec (openapi-generator in
// pom.xml/build.gradle), the mapping annotations live in generated code the
// source scan never sees — the spec is the real source. Endpoints from the spec
// are added at `likely` (the spec drives generation, but only the build proves
// it), deduped against endpoints already found in code.
//
// Repos that merely HAVE an openapi file (hand-written docs, which drift) are
// not ingested — the gate is the generator plugin in the build file.
func (*Provider) IngestSpecs(ic *provider.IndexContext, svc *model.Service) error {
	if !usesOpenAPIGenerator(ic.Files) {
		return nil
	}

	seen := map[string]bool{}
	for _, e := range svc.Endpoints {
		seen[e.Method+" "+e.Path] = true
	}

	for _, p := range sortedOpenAPIPaths(ic.Parsed) {
		eps, err := contract.ParseOpenAPI(ic.Parsed[p].(*rawFile).Src())
		if err != nil {
			continue // a broken spec never fails the scan (best-effort source)
		}
		for _, e := range eps {
			if seen[e.Method+" "+e.Path] {
				continue // code already declares it — code wins
			}
			seen[e.Method+" "+e.Path] = true
			e.Protocol = model.ProtoREST
			e.Detection = model.DetectOpenAPI
			e.Confidence = model.Likely
			svc.Endpoints = append(svc.Endpoints, e)
		}
	}
	return nil
}

// usesOpenAPIGenerator reports whether the service build generates code from a
// spec (openapi-generator maven/gradle plugin).
func usesOpenAPIGenerator(fs provider.FileTree) bool {
	for _, build := range []string{"pom.xml", "build.gradle", "build.gradle.kts"} {
		if b, err := fs.Read(build); err == nil && bytes.Contains(b, []byte("openapi-generator")) {
			return true
		}
	}
	return false
}

func sortedOpenAPIPaths(parsed map[string]provider.ParsedFile) []string {
	var out []string
	for p, pf := range parsed {
		if rf, ok := pf.(*rawFile); ok && rf.Kind() == provider.KindOpenAPI {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

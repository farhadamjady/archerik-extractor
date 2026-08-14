package micronaut

import (
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/java"
)

// contractIndexer builds Index.HTTPContracts: for every API interface (or
// abstract class) declared in the service OR a sibling *-api module, the set of
// its methods that carry an HTTP mapping annotation (@Get/@Post/...). Micronaut's
// idiomatic pattern declares the HTTP contract on an interface and lets the
// @Controller merely `implements` it, so the mappings live off the controller —
// frequently in another module the detector never scans. Storing the method
// nodes here lets the REST detector compose them with the controller base path.
type contractIndexer struct{}

func (contractIndexer) Name() string { return "micronaut.contracts" }

func (contractIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	contracts := map[string][]provider.ASTNode{}
	// Include shared sibling-module files (the *-api modules the interfaces live
	// in) as well as the service's own. Deterministic: sorted file iteration.
	for _, jf := range javaFilesOf(ic) {
		indexContractsIn(jf, contracts)
	}
	if len(contracts) > 0 {
		idx.HTTPContracts = contracts
	}
	return nil
}

// indexContractsIn records the HTTP-mapped methods of every interface/class
// declaration in one file, keyed by the declaration's simple name.
func indexContractsIn(jf *java.File, out map[string][]provider.ASTNode) {
	jf.Root().Walk(func(n java.Node) bool {
		switch n.Type() {
		case "interface_declaration", "class_declaration":
			name := n.ChildByFieldName("name").Text()
			if name == "" {
				return true
			}
			if methods := httpMappedMethods(n); len(methods) > 0 {
				for _, m := range methods {
					out[name] = append(out[name], m)
				}
			}
		}
		return true // keep walking (nested types can be contracts too)
	})
}

// httpMappedMethods returns the method_declaration nodes of a type whose
// modifiers carry one of the Micronaut HTTP mapping annotations.
func httpMappedMethods(typeDecl java.Node) []java.Node {
	body := typeDecl.ChildByFieldName("body")
	if !body.Valid() {
		return nil
	}
	var out []java.Node
	for _, m := range java.NamedChildren(body) {
		if m.Type() != "method_declaration" {
			continue
		}
		mods := java.ChildByType(m, "modifiers")
		if !mods.Valid() {
			continue
		}
		for _, a := range java.AnnotationsOf(mods) {
			if mappingVerb[java.AnnotationName(a)] != "" {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

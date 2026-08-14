package quarkus

import (
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/java"
)

// contractIndexer builds Index.HTTPContracts for the JAX-RS API-interface
// pattern: a @Path resource `implements` an interface whose methods carry the
// verb (@GET/@POST/...) + @Path annotations. That interface is frequently
// OpenAPI-generated or hand-written in a sibling module. Storing the mapped
// method nodes lets the REST detector compose them with the resource base path.
type contractIndexer struct{}

func (contractIndexer) Name() string { return "quarkus.contracts" }

func (contractIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	contracts := map[string][]provider.ASTNode{}
	for _, jf := range javaFilesOf(ic) {
		indexContractsIn(jf, contracts)
	}
	if len(contracts) > 0 {
		idx.HTTPContracts = contracts
	}
	return nil
}

func indexContractsIn(jf *java.File, out map[string][]provider.ASTNode) {
	jf.Root().Walk(func(n java.Node) bool {
		switch n.Type() {
		case "interface_declaration", "class_declaration":
			name := n.ChildByFieldName("name").Text()
			if name == "" {
				return true
			}
			for _, m := range verbMethods(n) {
				out[name] = append(out[name], m)
			}
		}
		return true
	})
}

// verbMethods returns the method_declaration nodes of a type whose modifiers
// carry a JAX-RS HTTP-verb annotation.
func verbMethods(typeDecl java.Node) []java.Node {
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
			if jaxrsVerbs[java.AnnotationName(a)] {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

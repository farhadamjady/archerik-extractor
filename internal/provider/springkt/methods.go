package springkt

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/kotlin"
	"github.com/farhadamjady/service-discovery/internal/schema"
)

// methodIndexer records each Kotlin type's method return types into
// Index.MethodReturns, so a controller handler with an EXPRESSION body and no
// declared return type (`fun findAll() = repository.findAll()`) can be typed from
// the delegate's return (#66). For Spring Data repository interfaces it also
// injects the standard CRUD method returns, bound to the entity type, since those
// methods are framework-inherited (not in source).
type methodIndexer struct{}

func (methodIndexer) Name() string { return "springkt.methods" }

func (methodIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	m := map[string]map[string]string{}
	index := func(files map[string]provider.ParsedFile) {
		for _, pf := range files {
			kf, ok := pf.(*kotlin.File)
			if !ok {
				continue
			}
			kf.Root().Walk(func(n kotlin.Node) bool {
				if n.Type() == "class_declaration" {
					indexTypeMethods(n, m)
				}
				return true
			})
		}
	}
	index(ic.Parsed)
	index(ic.Shared)
	idx.MethodReturns = m
	return nil
}

// indexTypeMethods records a type's declared method returns and, when it is a
// Spring Data repository, its synthetic CRUD returns.
func indexTypeMethods(decl kotlin.Node, m map[string]map[string]string) {
	name := kotlin.ChildByType(decl, "type_identifier")
	if !name.Valid() {
		return
	}
	byMethod := m[name.Text()]
	if byMethod == nil {
		byMethod = map[string]string{}
		m[name.Text()] = byMethod
	}
	if body := kotlin.ChildByType(decl, "class_body"); body.Valid() {
		for _, fn := range kotlin.NamedChildren(body) {
			if fn.Type() != "function_declaration" {
				continue
			}
			fnName := kotlin.ChildByType(fn, "simple_identifier")
			if rt, _ := kotlin.ReturnType(fn); fnName.Valid() && rt != "" {
				if _, ok := byMethod[fnName.Text()]; !ok {
					byMethod[fnName.Text()] = rt
				}
			}
		}
	}
	if entity, ok := repositoryEntity(decl); ok {
		for method, ret := range springDataReturns(entity) {
			if _, exists := byMethod[method]; !exists {
				byMethod[method] = ret
			}
		}
	}
}

// springDataRepos are the Spring Data base interfaces whose CRUD methods are
// inherited (and so absent from the repository's own source).
var springDataRepos = map[string]bool{
	"CrudRepository": true, "JpaRepository": true, "PagingAndSortingRepository": true,
	"ReactiveCrudRepository": true, "CoroutineCrudRepository": true, "ListCrudRepository": true,
}

// repositoryEntity returns the entity type of a repository interface — the first
// type argument of a Spring Data base (`CrudRepository<Customer, Long>` ->
// "Customer"), and whether the type is such a repository.
func repositoryEntity(decl kotlin.Node) (string, bool) {
	for _, c := range kotlin.NamedChildren(decl) {
		if c.Type() != "delegation_specifier" {
			continue
		}
		ut := kotlin.ChildByType(c, "user_type")
		if !ut.Valid() {
			continue
		}
		base := kotlin.ChildByType(ut, "type_identifier")
		targs := kotlin.ChildByType(ut, "type_arguments")
		if !base.Valid() || !springDataRepos[base.Text()] || !targs.Valid() {
			continue
		}
		// type_arguments -> type_projection -> user_type (the entity, first arg).
		if proj := kotlin.ChildByType(targs, "type_projection"); proj.Valid() {
			if e := kotlin.ChildByType(proj, "user_type"); e.Valid() {
				return e.Text(), true
			}
		}
	}
	return "", false
}

// springDataReturns is the return-type text of the standard Spring Data methods,
// bound to the entity type E. Query-derived methods (findByX) are not modeled —
// they are declared in source and indexed directly.
func springDataReturns(entity string) map[string]string {
	return map[string]string{
		"findAll":          "Iterable<" + entity + ">",
		"findAllById":      "Iterable<" + entity + ">",
		"saveAll":          "Iterable<" + entity + ">",
		"findById":         "Optional<" + entity + ">",
		"getById":          entity,
		"getOne":           entity,
		"getReferenceById": entity,
		"save":             entity,
		"saveAndFlush":     entity,
	}
}

// controllerFields maps a controller's injected dependency names to their declared
// types — primary-constructor properties and class-body properties — so a
// `repository.method(...)` expression body can resolve the receiver's type (#66).
func controllerFields(class kotlin.Node) map[string]string {
	out := map[string]string{}
	if pc := kotlin.ChildByType(class, "primary_constructor"); pc.Valid() {
		for _, p := range kotlin.NamedChildren(pc) {
			if p.Type() != "class_parameter" {
				continue
			}
			name := kotlin.ChildByType(p, "simple_identifier")
			if typ, _ := kotlin.DeclaredType(p); name.Valid() && typ != "" {
				out[name.Text()] = typ
			}
		}
	}
	if body := kotlin.ChildByType(class, "class_body"); body.Valid() {
		for _, m := range kotlin.NamedChildren(body) {
			if m.Type() != "property_declaration" {
				continue
			}
			vd := kotlin.ChildByType(m, "variable_declaration")
			name := kotlin.ChildByType(vd, "simple_identifier")
			if typ, _ := kotlin.DeclaredType(vd); name.Valid() && typ != "" {
				out[name.Text()] = typ
			}
		}
	}
	return out
}

// inferResponseFromExprBody types an expression-body handler (`fun f(...) =
// receiver.method(...)`) that has no declared return type: it resolves the
// receiver field's type and that type's method return, then walks it (#66).
func inferResponseFromExprBody(fn kotlin.Node, ctrlFields map[string]string, methodReturns map[string]map[string]string, w *schema.Walker) *model.Schema {
	body := kotlin.ChildByType(fn, "function_body")
	if !body.Valid() {
		return nil
	}
	var call kotlin.Node
	body.Walk(func(n kotlin.Node) bool {
		if call.Valid() {
			return false
		}
		if n.Type() == "call_expression" {
			call = n
			return false
		}
		return true
	})
	if !call.Valid() {
		return nil
	}
	nav := kotlin.ChildByType(call, "navigation_expression")
	if !nav.Valid() {
		return nil
	}
	recv, sel := navReceiverSelector(nav)
	if recv == "" || sel == "" {
		return nil
	}
	ft, ok := ctrlFields[recv]
	if !ok {
		return nil
	}
	byMethod, ok := methodReturns[simpleType(ft)]
	if !ok {
		return nil
	}
	rt, ok := byMethod[sel]
	if !ok {
		return nil
	}
	return bodyOrNil(w.Type(rt), false)
}

// navReceiverSelector splits `receiver.selector` from a navigation expression:
// the receiver is the leading identifier, the selector the trailing one.
func navReceiverSelector(nav kotlin.Node) (recv, sel string) {
	kids := kotlin.NamedChildren(nav)
	if len(kids) > 0 && kids[0].Type() == "simple_identifier" {
		recv = kids[0].Text()
	}
	nav.Walk(func(x kotlin.Node) bool {
		if x.Type() == "simple_identifier" {
			sel = x.Text() // last identifier is the navigated member
		}
		return true
	})
	return recv, sel
}

// simpleType drops generic args and any qualifier from a type text.
func simpleType(t string) string {
	t = strings.TrimSpace(t)
	if i := strings.IndexByte(t, '<'); i >= 0 {
		t = t[:i]
	}
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		t = t[i+1:]
	}
	return strings.TrimSpace(t)
}

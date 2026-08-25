package laravel

import (
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/php"
)

// classIndexer records every PHP class declaration by simple name, so a detector
// can follow a class NAME into the file that declares it. Laravel spreads one
// endpoint's contract across three files that never reference each other's
// paths:
//
//	routes/api.php     Route::put('user', [UserController::class, 'update'])
//	UserController.php public function update(UpdateUserRequest $request)
//	                   { ... return new UserResource($user); }
//	UpdateUserRequest  public function rules() { return ['email' => ...]; }
//	UserResource       public function toArray($r) { return ['email' => ...]; }
//
// The request and response contracts are in the last two, reachable only by
// class name. This is the same cross-file hop `Index.GoFuncBodies` exists for.
type classIndexer struct{}

func (classIndexer) Name() string { return "laravel.class" }

func (classIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	classes := map[string]provider.ASTNode{}
	for _, p := range sortedPHPPaths(ic.Parsed) {
		f, ok := ic.Parsed[p].(*php.File)
		if !ok {
			continue
		}
		ns := f.Namespace()
		f.Root().Walk(func(n php.Node) bool {
			if n.Type() != "class_declaration" {
				return true
			}
			name := php.ChildByType(n, "name").Text()
			if name == "" {
				return true
			}
			// Keyed by fully-qualified name, which is unique, AND by simple name
			// as a fallback for a reference this analysis cannot qualify.
			// Path-sorted iteration makes the simple-name first-wins
			// deterministic.
			if ns != "" {
				classes[ns+`\`+name] = n
			}
			if _, taken := classes[name]; !taken {
				classes[name] = n
			}
			return true
		})
	}
	if len(classes) > 0 {
		idx.PHPClasses = classes
	}
	return nil
}

// phpClass is the read side of the index: a class declaration with the accessors
// the schema detectors need.
type phpClass struct {
	node php.Node
	idx  *provider.Index
}

// lookupClass resolves a class reference to its declaration.
//
// ctx is the node the name was written at, and it matters: a bare
// `PostController` means whatever that FILE's `use` line says it means.
// laravel-blog declares three classes by that name (web, admin, API v1), so
// resolving on the simple name alone reads the wrong controller — and the wrong
// contract — for two thirds of them. Resolution order mirrors PHP's own:
//
//  1. an already-qualified name;
//  2. the file's `use` imports;
//  3. the file's own namespace (a sibling class needs no import);
//  4. the simple name, as a last resort.
func lookupClass(idx *provider.Index, ctx php.Node, name string) (phpClass, bool) {
	if idx == nil || name == "" {
		return phpClass{}, false
	}
	name = strings.TrimSuffix(strings.TrimSpace(name), "::class")
	name = strings.TrimPrefix(name, `\`)
	if name == "" {
		return phpClass{}, false
	}
	var candidates []string
	if strings.Contains(name, `\`) {
		candidates = append(candidates, name)
	} else if f := ctx.File(); f != nil {
		if fqn, ok := f.Imports()[name]; ok {
			candidates = append(candidates, fqn)
		}
		if ns := f.Namespace(); ns != "" {
			candidates = append(candidates, ns+`\`+name)
		}
	}
	candidates = append(candidates, simpleClassName(name))
	for _, c := range candidates {
		if n, ok := idx.PHPClasses[c].(php.Node); ok && n.Valid() {
			return phpClass{node: n, idx: idx}, true
		}
	}
	return phpClass{}, false
}

// simpleClassName strips a namespace qualifier and a `::class` suffix, so
// `\App\Http\Resources\UserResource` and `UserResource::class` both key on
// `UserResource`.
func simpleClassName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, "::class")
	if i := strings.LastIndexByte(name, '\\'); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimSpace(name)
}

// name returns the class's own declared simple name.
func (c phpClass) name() string { return php.ChildByType(c.node, "name").Text() }

// method returns a method declared on the class, or inherited from an ancestor
// this repo also declares. Laravel resources routinely put shared fields on a
// base class (realworld's BaseUserResource), so stopping at the declared class
// would read half a contract as a whole one.
func (c phpClass) method(name string) (php.Node, bool) {
	for cur, depth := c, 0; cur.node.Valid() && depth < maxInheritanceDepth; depth++ {
		if body := php.ChildByType(cur.node, "declaration_list"); body.Valid() {
			for _, d := range php.NamedChildren(body) {
				if d.Type() == "method_declaration" && php.ChildByType(d, "name").Text() == name {
					return d, true
				}
			}
		}
		next, ok := lookupClass(cur.idx, cur.node, cur.parentName())
		if !ok {
			break
		}
		cur = next
	}
	return php.Node{}, false
}

// maxInheritanceDepth bounds the walk up the `extends` chain. A cycle is not
// legal PHP, but a malformed or partially-scanned repo can still present one.
const maxInheritanceDepth = 8

// parentName returns the class this one extends, or "".
func (c phpClass) parentName() string {
	base := php.ChildByType(c.node, "base_clause")
	if !base.Valid() {
		return ""
	}
	return base.Text()[len("extends "):]
}

// extends reports whether the class descends from the named class, following the
// `extends` chain as far as this repo declares it. Used to recognize a
// FormRequest or a JsonResource, both of which are identified by their base.
func (c phpClass) extends(ancestor string) bool {
	for cur, depth := c, 0; depth < maxInheritanceDepth; depth++ {
		parent := simpleClassName(cur.parentName())
		if parent == "" {
			return false
		}
		if parent == ancestor {
			return true
		}
		next, ok := lookupClass(cur.idx, cur.node, parent)
		if !ok {
			return false
		}
		cur = next
	}
	return false
}

// propertyExpr returns the raw initializer TEXT of a property, for initializers
// that are not strings — `public $collects = ArticleResource::class`.
func (c phpClass) propertyExpr(name string) (string, bool) {
	init, ok := c.propertyInit(name)
	if !ok {
		return "", false
	}
	for _, v := range php.NamedChildren(init) {
		return v.Text(), true
	}
	return "", false
}

// staticString returns the value of a static string property (`public static
// $wrap = 'user'`), searching ancestors too.
func (c phpClass) staticString(name string) (string, bool) {
	init, ok := c.propertyInit(name)
	if !ok {
		return "", false
	}
	for _, v := range php.NamedChildren(init) {
		if s, ok := php.StringLit(v); ok {
			return s, true
		}
	}
	return "", false
}

// propertyInit finds a property's initializer node, searching ancestors too.
func (c phpClass) propertyInit(name string) (php.Node, bool) {
	for cur, depth := c, 0; cur.node.Valid() && depth < maxInheritanceDepth; depth++ {
		if body := php.ChildByType(cur.node, "declaration_list"); body.Valid() {
			for _, d := range php.NamedChildren(body) {
				if d.Type() != "property_declaration" {
					continue
				}
				for _, el := range php.NamedChildren(d) {
					if el.Type() != "property_element" {
						continue
					}
					if php.VarName(php.ChildByType(el, "variable_name")) != name {
						continue
					}
					if init := php.ChildByType(el, "property_initializer"); init.Valid() {
						return init, true
					}
				}
			}
		}
		next, ok := lookupClass(cur.idx, cur.node, cur.parentName())
		if !ok {
			break
		}
		cur = next
	}
	return php.Node{}, false
}

// returnedExpr returns the expression of the FIRST `return <expr>` in a method
// body. Right for a method with one answer — rules(), toArray() — and wrong for
// a handler, which has an error branch; see returnedExprs.
func returnedExpr(method php.Node) php.Node {
	if all := returnedExprs(method); len(all) > 0 {
		return all[0]
	}
	return php.Node{}
}

// returnedExprs returns every `return <expr>` in a method body, in source order,
// skipping returns inside nested closures (which belong to the closure, not the
// method).
func returnedExprs(method php.Node) []php.Node {
	body := php.ChildByType(method, "compound_statement")
	if !body.Valid() {
		return nil
	}
	var out []php.Node
	body.Walk(func(n php.Node) bool {
		switch n.Type() {
		case "anonymous_function_creation_expression", "arrow_function":
			return false // a closure's return is not the method's
		case "return_statement":
			for _, k := range php.NamedChildren(n) {
				out = append(out, k)
				break
			}
			return false
		}
		return true
	})
	return out
}

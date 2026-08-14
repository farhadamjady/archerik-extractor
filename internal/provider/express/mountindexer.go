package express

import (
	"path"
	"sort"
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/tsjs"
)

// mountIndexer builds Index.MountPrefixes: the real HTTP path
// of an Express route is its declared path PREFIXED by the app.use/router.use
// mounts, which are usually cross-file and nested —
//
//	config/express.js:  app.use('/v1', routes)          // routes = require('../api/routes/v1')
//	routes/v1/index.js: router.use('/users', userRoutes) // userRoutes = require('./user.route')
//	user.route.js:      router.route('/:userId').get(...) -> GET /v1/users/{userId}
//
// The indexer resolves each file's require()/import specifiers to their target
// files, records every `X.use('/prefix', <importedRouter>)` as a mount edge, and
// composes the transitive prefixes per file (cycle-guarded, depth-capped).
type mountIndexer struct{}

func (mountIndexer) Name() string { return "express.mounts" }

// maxMountDepth bounds nested mount chains; real apps nest 2-3 deep.
const maxMountDepth = 6

// mountEdge is one `use('/prefix', router)` wiring: mounter mounts target.
type mountEdge struct {
	mounter string // repo-relative path of the file doing the .use()
	prefix  string
	target  string // repo-relative path of the mounted router's file
}

func (mountIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	// Sorted file iteration for determinism.
	var files []string
	for p, pf := range ic.Parsed {
		if _, ok := pf.(*tsjs.File); ok {
			files = append(files, p)
		}
	}
	sort.Strings(files)

	var edges []mountEdge
	for _, fp := range files {
		jf := ic.Parsed[fp].(*tsjs.File)
		imports := importSpecs(jf)
		edges = append(edges, mountsIn(jf, fp, imports, ic.Parsed)...)
	}
	if len(edges) == 0 {
		return nil
	}

	// mountedBy: target file -> the (mounter, prefix) pairs that mount it.
	mountedBy := map[string][]mountEdge{}
	for _, e := range edges {
		mountedBy[e.target] = append(mountedBy[e.target], e)
	}

	prefixes := map[string][]string{}
	for target := range mountedBy {
		ps := composePrefixes(target, mountedBy, 0, map[string]bool{})
		sort.Strings(ps)
		prefixes[target] = dedupe(ps)
	}
	idx.MountPrefixes = prefixes
	return nil
}

// composePrefixes returns every full prefix a file is reachable under: for each
// (mounter, prefix) mounting it, the mounter's own prefixes + prefix.
func composePrefixes(file string, mountedBy map[string][]mountEdge, depth int, seen map[string]bool) []string {
	if depth >= maxMountDepth || seen[file] {
		return []string{""}
	}
	seen[file] = true
	defer delete(seen, file)

	mounts := mountedBy[file]
	if len(mounts) == 0 {
		return []string{""}
	}
	var out []string
	for _, m := range mounts {
		for _, parent := range composePrefixes(m.mounter, mountedBy, depth+1, seen) {
			out = append(out, joinPrefix(parent, m.prefix))
		}
	}
	return out
}

func joinPrefix(parent, prefix string) string {
	p := strings.TrimSuffix(parent, "/") + "/" + strings.Trim(prefix, "/")
	return strings.TrimSuffix(p, "/")
}

// importSpecs maps a file's local identifiers to their import specifiers:
// `const x = require('./y')` and `import x from './y'`.
func importSpecs(jf *tsjs.File) map[string]string {
	specs := map[string]string{}
	jf.Root().Walk(func(n tsjs.Node) bool {
		switch n.Type() {
		case "variable_declarator":
			name := n.ChildByFieldName("name")
			val := n.ChildByFieldName("value")
			if name.Type() == "identifier" && val.Type() == "call_expression" &&
				val.ChildByFieldName("function").Text() == "require" {
				if args := val.ChildByFieldName("arguments"); args.Valid() {
					if kids := tsjs.NamedChildren(args); len(kids) == 1 && kids[0].Type() == "string" {
						specs[name.Text()] = tsjs.StringValue(kids[0])
					}
				}
			}
			return false
		case "import_statement":
			var spec string
			var names []string
			for _, c := range tsjs.NamedChildren(n) {
				switch c.Type() {
				case "string":
					spec = tsjs.StringValue(c)
				case "import_clause":
					c.Walk(func(x tsjs.Node) bool {
						if x.Type() == "identifier" {
							names = append(names, x.Text())
						}
						return true
					})
				}
			}
			for _, nm := range names {
				specs[nm] = spec
			}
			return false
		}
		return true
	})
	return specs
}

// mountsIn finds `X.use('/prefix', <identifier>)` calls whose identifier is an
// imported module that resolves to a parsed file, and returns the mount edges.
func mountsIn(jf *tsjs.File, filePath string, imports map[string]string, parsed map[string]provider.ParsedFile) []mountEdge {
	var out []mountEdge
	jf.Root().Walk(func(n tsjs.Node) bool {
		if n.Type() != "call_expression" {
			return true
		}
		fn := n.ChildByFieldName("function")
		if fn.Type() != "member_expression" || fn.ChildByFieldName("property").Text() != "use" {
			return true
		}
		args := tsjs.NamedChildren(n.ChildByFieldName("arguments"))
		if len(args) < 2 || args[0].Type() != "string" {
			return true
		}
		prefix := tsjs.StringValue(args[0])
		if !strings.HasPrefix(prefix, "/") {
			return true
		}
		// The mounted router may be any later argument (middleware in between).
		for _, a := range args[1:] {
			if a.Type() != "identifier" {
				continue
			}
			spec, ok := imports[a.Text()]
			if !ok {
				continue
			}
			if target, ok := resolveModule(filePath, spec, parsed); ok {
				out = append(out, mountEdge{mounter: filePath, prefix: prefix, target: target})
			}
		}
		return true
	})
	return out
}

// resolveModule resolves a relative import specifier against the importing
// file's directory, Node-style: exact file, +.js/.ts, or /index.js|.ts. Only
// relative specs resolve (a bare 'express' is a package, not a file).
func resolveModule(fromFile, spec string, parsed map[string]provider.ParsedFile) (string, bool) {
	if !strings.HasPrefix(spec, ".") {
		return "", false
	}
	base := path.Join(path.Dir(fromFile), spec)
	for _, cand := range []string{
		base, base + ".js", base + ".ts", base + ".mjs",
		path.Join(base, "index.js"), path.Join(base, "index.ts"),
	} {
		if _, ok := parsed[cand]; ok {
			return cand, true
		}
	}
	return "", false
}

func dedupe(ss []string) []string {
	out := ss[:0]
	var last string
	for i, s := range ss {
		if i == 0 || s != last {
			out = append(out, s)
		}
		last = s
	}
	return out
}

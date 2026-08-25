package laravel

import (
	"path"
	"sort"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/php"
)

// prefixIndexer resolves the URL prefix each ROUTE FILE is mounted under, which
// Laravel declares outside the route file itself. `routes/api.php` holding
// `Route::get('/users', ...)` serves `/api/users`, and nothing in that file says
// so. Two declaration sites, by Laravel generation:
//
//   - Laravel 11+: bootstrap/app.php —
//     `->withRouting(web: __DIR__.'/../routes/web.php', api: __DIR__.'/../routes/api.php',
//     apiPrefix: 'api/v1')`, where apiPrefix defaults to `api`.
//   - Laravel <=10: app/Providers/RouteServiceProvider.php —
//     `Route::prefix('api')->group(base_path('routes/api.php'))`, which also
//     covers the custom route files a service adds (routes/admin.php, ...).
//
// When neither declares a file, the framework defaults apply: routes/api.php is
// mounted under `api`, routes/web.php at the root. That is a fixed framework
// fact, not a guess, so the endpoints stay `confirmed`.
//
// Results land in the shared Index.MountPrefixes (the same field the Express
// mount indexer populates) and the route detector prepends them.
type prefixIndexer struct{}

func (prefixIndexer) Name() string { return "laravel.prefix" }

func (prefixIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	claims := map[string][]string{}
	for _, p := range sortedPHPPaths(ic.Parsed) {
		f, ok := ic.Parsed[p].(*php.File)
		if !ok {
			continue
		}
		f.Root().Walk(func(n php.Node) bool {
			if !php.IsCall(n) {
				return true
			}
			switch php.CallName(n) {
			case "withRouting":
				readWithRouting(n, claims)
			case "group":
				readMountingGroup(n, claims)
			}
			return true
		})
	}

	// Framework defaults, for a route file nothing claimed.
	for _, p := range sortedPHPPaths(ic.Parsed) {
		if _, claimed := claims[p]; claimed {
			continue
		}
		switch p {
		case "routes/api.php":
			claims[p] = []string{"api"}
		case "routes/web.php":
			claims[p] = []string{""}
		}
	}

	if len(claims) == 0 {
		return nil
	}
	if idx.MountPrefixes == nil {
		idx.MountPrefixes = map[string][]string{}
	}
	for p, prefixes := range claims {
		sort.Strings(prefixes)
		idx.MountPrefixes[p] = prefixes
	}
	return nil
}

// readWithRouting reads the Laravel 11+ bootstrap/app.php routing declaration.
func readWithRouting(call php.Node, claims map[string][]string) {
	apiPrefix := "api" // the framework default when apiPrefix is not passed
	if v := php.NamedArg(call, "apiPrefix"); v.Valid() {
		if s, ok := php.StringLit(v); ok {
			apiPrefix = s
		}
	}
	for _, arg := range []struct {
		name   string
		prefix string
	}{{"api", apiPrefix}, {"web", ""}} {
		v := php.NamedArg(call, arg.name)
		if !v.Valid() {
			continue
		}
		if rel, ok := routesFile(v); ok {
			claim(claims, rel, arg.prefix)
		}
	}
}

// readMountingGroup reads a `->group(<route file>)` mount: the Laravel <=10
// RouteServiceProvider form, where the group's argument is a PATH to a route
// file rather than a closure. A group whose argument is a closure is an ordinary
// in-file route group and is handled by the detector, not here.
func readMountingGroup(call php.Node, claims map[string][]string) {
	for i := 0; i < 2; i++ {
		v := php.PositionalArg(call, i)
		if !v.Valid() {
			continue
		}
		switch v.Type() {
		case "anonymous_function_creation_expression", "arrow_function":
			continue
		}
		if rel, ok := routesFile(v); ok {
			claim(claims, rel, groupPrefix(call))
			return
		}
	}
}

// routesFile extracts the route file an expression points at —
// `base_path('routes/api.php')`, `__DIR__.'/../routes/api.php'` — as the
// repo-relative path the scanner keys parsed files by.
func routesFile(expr php.Node) (string, bool) {
	var lit string
	expr.Walk(func(n php.Node) bool {
		if lit != "" {
			return false
		}
		if s, ok := php.StringLit(n); ok && strings.HasSuffix(s, ".php") {
			lit = s
		}
		return true
	})
	if lit == "" {
		return "", false
	}
	lit = strings.ReplaceAll(lit, `\`, "/")
	if i := strings.LastIndex(lit, "routes/"); i >= 0 {
		return lit[i:], true
	}
	return "routes/" + path.Base(lit), true
}

// claim records one mount prefix for a route file, ignoring an exact duplicate.
// A file mounted twice (two prefixes) keeps both: the routes really are served
// at both, so the detector emits one endpoint per prefix.
func claim(claims map[string][]string, rel, prefix string) {
	for _, p := range claims[rel] {
		if p == prefix {
			return
		}
	}
	claims[rel] = append(claims[rel], prefix)
}

// sortedPHPPaths returns the parsed PHP files in stable path order, so indexing
// is deterministic.
func sortedPHPPaths(parsed map[string]provider.ParsedFile) []string {
	var paths []string
	for p, pf := range parsed {
		if _, ok := pf.(*php.File); ok {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths
}

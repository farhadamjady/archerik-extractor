// Package laravel is the Laravel (PHP) framework provider, on the Recipe-B
// lang/php layer. Laravel declares routes with CALLS in dedicated route files —
// `Route::get('/users/{id}', [UserController::class, 'show'])` — so the detector
// is call-based like Express and net/http, with two twists neither of those has:
// prefixes compose through CHAINED group calls and closures
// (`Route::prefix('v1')->group(function () { ... })`), and resource registrars
// (`Route::apiResource('posts', PostController::class)`) expand to a fixed set of
// endpoints that appear nowhere in the source as literal paths.
//
// Lumen (`$router->get(...)`, a different router) is deliberately not matched.
package laravel

import (
	"bytes"
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/php"
)

// Provider detects and extracts from Laravel services.
type Provider struct{}

// New returns the Laravel provider.
func New() *Provider { return &Provider{} }

func (*Provider) Name() string { return "laravel-php" }

// Language is PHP.
func (*Provider) Language() string { return "PHP" }

// Match scores a Laravel repo. It requires three things, and a dependency string
// is only one of them:
//
//  1. PHP sources;
//  2. a framework marker — the `laravel/framework` composer dependency or the
//     `artisan` console entry point — so a plain PHP repo (or a Symfony one,
//     whose routing is attribute-based and out of scope) never matches;
//  3. an actual Route facade REGISTRATION in a file this provider would extract
//     from.
//
// (3) is the usage gate, and it is what keeps the very large population of
// Laravel *packages* out. A `spatie/laravel-*` style repo carries
// `laravel/framework` in require-dev and declares no routes of its own; without
// the gate it matched on the dependency alone, claimed the repo, found nothing,
// and emitted an empty graph at exit 0 — which is a valid graph, so the failure
// was silent. Refusing to match is the honest answer: detection then fails loudly
// with exit 2. See docs/CROSS-STACK-CHECKS.md, Check 2.
//
// The consequence worth stating: a Laravel app that serves no HTTP at all (a
// console- or queue-worker-only app that has deleted its route files) no longer
// matches. Today REST is the whole of this provider's output, so declining is
// correct — there is genuinely nothing to extract. When outbound clients and
// schemas land, this gate should widen to accept those usages too.
//
// A Lumen repo is explicitly declined: it ships a different router this provider
// does not read.
func (*Provider) Match(root string, fs provider.FileTree) (bool, int) {
	srcs := fs.Glob("**/*.php")
	if len(srcs) == 0 {
		return false, 0
	}
	composer, _ := fs.Read("composer.json")
	if bytes.Contains(composer, []byte("laravel/lumen-framework")) {
		return false, 0
	}
	// Quoted, so only a package-name key counts — not a prose mention of the
	// framework in "description" or "keywords". (laravel-zero/framework, the CLI
	// framework with an artisan-like entry point and no HTTP router, already
	// fails the bare substring; it is the usage gate below that declines it.)
	framework := bytes.Contains(composer, []byte(`"laravel/framework"`))
	artisan := fs.Exists("artisan")
	if !framework && !artisan {
		return false, 0
	}
	if !registersRoutesAnywhere(fs, srcs) {
		return false, 0
	}
	score := 1
	if framework {
		score += 3
	}
	if artisan {
		score += 2
	}
	if fs.Exists("routes/api.php") || fs.Exists("routes/web.php") {
		score += 2
	}
	return true, score
}

// registersRoutesAnywhere reports whether any extractable PHP source registers a
// route. Files excluded by FileSpec are skipped, because usage in a file we will
// never parse is not usage: vendor/ carries the framework's own route
// declarations (and every installed package's), and a package repo's test suite
// routinely spins up throwaway routes — counting either would reinstate exactly
// the false positive the gate exists to prevent.
func registersRoutesAnywhere(fs provider.FileTree, srcs []string) bool {
	for _, f := range srcs {
		if excludedFromExtraction(f) {
			continue
		}
		if b, err := fs.Read(f); err == nil && registersRoutes(b) {
			return true
		}
	}
	return false
}

// excludedFromExtraction mirrors FileSpec.Exclude for the path forms that matter
// during Match, which runs before the scanner applies those globs.
func excludedFromExtraction(path string) bool {
	for _, dir := range []string{"vendor/", "node_modules/", "storage/", "bootstrap/cache/", "tests/"} {
		if strings.HasPrefix(path, dir) || strings.Contains(path, "/"+dir) {
			return true
		}
	}
	return strings.HasSuffix(path, "Test.php")
}

// registersRoutes reports whether a PHP source CALLS the Route facade in a way
// that registers something — a registration (`Route::get(...)`,
// `Route::apiResource(...)`) or a chain starter (`Route::prefix('v1')->group()`).
// The `\Route::` and `Illuminate\Support\Facades\Route::` forms end in the same
// suffix, so one needle covers all three.
//
// Two things it deliberately rejects, so this gate agrees with what the detector
// would actually act on:
//
//   - `Route::class`, which appears in the `aliases` array of a stock
//     config/app.php and registers nothing — hence the call requirement;
//   - `Route::getRoutes()`, `Route::has()`, `Route::current()` — the facade's
//     INSPECTION methods. A package that only reads the route table (a
//     route-listing or debug tool) is not a service that serves routes.
//
// This is a byte scan, not a parse, so it counts a registration written inside a
// comment or a docblock example. Match runs on every candidate repo before any
// parsing, and the cost of an AST pass over a whole repo is not worth removing a
// false positive that still has to clear the framework-marker gate above.
func registersRoutes(src []byte) bool {
	const needle = "Route::"
	for i := 0; ; {
		j := bytes.Index(src[i:], []byte(needle))
		if j < 0 {
			return false
		}
		i += j + len(needle)
		if name, ok := calledMethod(src[i:]); ok && routeChainMethod(name) {
			return true
		}
	}
}

// calledMethod reads a method name applied to arguments at the start of src — an
// identifier, then optional spacing, then `(` — and reports whether one is there.
func calledMethod(src []byte) (string, bool) {
	n := 0
	for n < len(src) && identByte(src[n]) {
		n++
	}
	if n == 0 {
		return "", false
	}
	name := string(src[:n])
	for n < len(src) && (src[n] == ' ' || src[n] == '\t') {
		n++
	}
	if n < len(src) && src[n] == '(' {
		return name, true
	}
	return "", false
}

func identByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// FileSpec collects PHP sources. Composer packages, tests, generated caches and
// front-end assets are excluded so they don't inflate the graph — vendor/ in
// particular carries the framework's own route declarations.
func (*Provider) FileSpec() provider.FileSpec {
	return provider.FileSpec{
		Groups: []provider.FileGroup{
			{Kind: provider.KindJava, Include: []string{"**/*.php"}},
		},
		Exclude: []string{
			"**/vendor/**",
			"**/node_modules/**",
			"**/storage/**",
			"**/bootstrap/cache/**",
			"**/tests/**",
			"**/*Test.php",
		},
	}
}

func (*Provider) Parsers() map[provider.FileKind]provider.Parser {
	return map[provider.FileKind]provider.Parser{
		provider.KindJava: php.NewParser(),
	}
}

// Indexers: the prefix indexer resolves which URL prefix each route FILE is
// mounted under (`routes/api.php` under `api`), which is declared outside the
// route file itself. Config resolution (`config()`/`env()`), outbound clients
// and schemas are next rounds.
func (*Provider) Indexers() []provider.Indexer {
	return []provider.Indexer{prefixIndexer{}}
}

// Detectors: REST endpoints from Route facade registrations, including group
// prefix composition and resource-registrar expansion.
func (*Provider) Detectors() []provider.Detector {
	return []provider.Detector{
		routeDetector{},
	}
}

func (*Provider) NewResolver(idx *provider.Index) provider.Resolver { return nil }

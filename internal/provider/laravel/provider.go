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

// Match scores a Laravel repo. It requires PHP sources plus a real framework
// marker — the `laravel/framework` composer dependency or the `artisan` console
// entry point — so a plain PHP repo (or a Symfony one, whose routing is
// attribute-based and out of scope) never matches. A Lumen repo is explicitly
// declined: it ships a different router this provider does not read.
func (*Provider) Match(root string, fs provider.FileTree) (bool, int) {
	if len(fs.Glob("**/*.php")) == 0 {
		return false, 0
	}
	composer, _ := fs.Read("composer.json")
	if bytes.Contains(composer, []byte("laravel/lumen-framework")) {
		return false, 0
	}
	framework := bytes.Contains(composer, []byte("laravel/framework"))
	artisan := fs.Exists("artisan")
	if !framework && !artisan {
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

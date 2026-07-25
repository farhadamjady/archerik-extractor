package express

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
	"github.com/farhadamjady/service-discovery/internal/query"
	"github.com/farhadamjady/service-discovery/internal/scan"
)

var _ provider.Provider = (*Provider)(nil)

func endpoints(t *testing.T, src string) []string {
	t.Helper()
	f, err := tsjs.NewParser().Parse("app.js", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{routeDetector{}}, &provider.Index{}, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
	var out []string
	for _, e := range svc.Endpoints {
		out = append(out, fmt.Sprintf("%s %s", e.Method, e.Path))
	}
	sort.Strings(out)
	return out
}

func TestRoutes(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "app + router verbs, :param normalized",
			src: `const app = express();
				app.get('/users', (req, res) => res.json(users));
				app.post('/users/:id', createUser);
				router.delete('/:id', handler);
				this.router.put('/items/:itemId', h);`,
			want: []string{"DELETE /{id}", "GET /users", "POST /users/{id}", "PUT /items/{itemId}"},
		},
		{
			name: "one-arg settings getter is not a route",
			src: `const port = app.get('port');
				app.get('/health', (req, res) => res.send('ok'));`,
			want: []string{"GET /health"},
		},
		{
			name: "unrelated .get on a map is ignored (chain receiver rejected)",
			src: `const v = cache.get('key', fallback);
				getRouter().post('/x', h);`,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := endpoints(t, tc.src)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchDefersToNest(t *testing.T) {
	// A NestJS repo (has @nestjs) must NOT be claimed by Express even though it
	// depends on express transitively.
	nest := t.TempDir()
	writeFile(t, nest, "package.json", `{"dependencies":{"@nestjs/core":"^10","express":"^4"}}`)
	writeFile(t, nest, "src/main.ts", "import 'express';")
	if m, _ := New().Match(nest, scan.NewOSFileTree(nest, nil)); m {
		t.Error("Express must defer to NestJS")
	}

	// A plain Express repo matches.
	exp := t.TempDir()
	writeFile(t, exp, "package.json", `{"dependencies":{"express":"^4"}}`)
	writeFile(t, exp, "index.js", "const express = require('express'); const app = express();")
	m, score := New().Match(exp, scan.NewOSFileTree(exp, nil))
	if !m || score != 5 { // express dep(2) + require('express')(3)
		t.Fatalf("plain Express: matched=%v score=%d, want true/5", m, score)
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestMountComposition locks IMPROVEMENTS #50: routes declared in a router file
// are emitted under the composed cross-file app.use/router.use prefixes.
func TestMountComposition(t *testing.T) {
	sources := map[string]string{
		"config/express.js":           `const routes = require('../api/routes/v1'); app.use('/v1', routes);`,
		"api/routes/v1/index.js":      `const userRoutes = require('./user.route'); router.use('/users', userRoutes); router.get('/status', h);`,
		"api/routes/v1/user.route.js": `router.route('/:userId').get(h).put(h);`,
	}
	parsed := map[string]provider.ParsedFile{}
	for p, src := range sources {
		f, err := tsjs.NewParser().Parse(p, []byte(src))
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		parsed[p] = f
	}
	idx := &provider.Index{}
	if err := (mountIndexer{}).Index(&provider.IndexContext{Parsed: parsed}, idx); err != nil {
		t.Fatalf("index: %v", err)
	}
	svc := model.NewService("s", "s", "")
	for _, p := range []string{"api/routes/v1/index.js", "api/routes/v1/user.route.js", "config/express.js"} {
		if err := query.New().Run(parsed[p], []provider.Detector{routeDetector{}}, idx, nil, svc); err != nil {
			t.Fatalf("run %s: %v", p, err)
		}
	}
	model.Sort(svc)
	var got []string
	for _, e := range svc.Endpoints {
		got = append(got, e.Method+" "+e.Path)
	}
	sort.Strings(got)
	want := []string{"GET /v1/status", "GET /v1/users/{userId}", "PUT /v1/users/{userId}"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

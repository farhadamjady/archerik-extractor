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

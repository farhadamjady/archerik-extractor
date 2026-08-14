package nestjs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/tsjs"
	"github.com/farhadamjady/archerik-extractor/internal/query"
	"github.com/farhadamjady/archerik-extractor/internal/scan"
)

var _ provider.Provider = (*Provider)(nil)

func endpoints(t *testing.T, src string) []string {
	t.Helper()
	f, err := tsjs.NewParser().Parse("c.ts", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{restDetector{}}, &provider.Index{}, nil, svc); err != nil {
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

func TestRESTEndpoints(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "controller base + method path, :param normalized to {param}",
			src: `@Controller('cats')
				export class CatsController {
					@Get(':id')
					findOne(@Param('id') id: string): Cat { return this.svc.findOne(id); }
					@Post()
					create(@Body() dto: CreateCatDto): Promise<Cat> { return this.svc.create(dto); }
				}`,
			want: []string{"GET /cats/{id}", "POST /cats"},
		},
		{
			name: "no base path; nested path",
			src: `@Controller()
				export class AppController {
					@Get('health/live') live(): string { return 'ok'; }
					@Delete('items/:itemId') del() {}
				}`,
			want: []string{"DELETE /items/{itemId}", "GET /health/live"},
		},
		{
			name: "non-controller class ignored",
			src: `@Injectable()
				export class CatsService {
					@Get('x') foo() {}
				}`,
			want: nil,
		},
		{
			name: "multiple verbs incl PUT/PATCH",
			src: `@Controller('orders')
				export class OrderController {
					@Put(':id') update() {}
					@Patch(':id/status') patch() {}
					@Get() all() {}
				}`,
			want: []string{"GET /orders", "PATCH /orders/{id}/status", "PUT /orders/{id}"},
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

func TestDetectors(t *testing.T) {
	want := map[string]model.Protocol{
		"nestjs.rest":   model.ProtoREST,
		"nestjs.client": model.ProtoREST,
		"nestjs.kafka":  model.ProtoKafka,
	}
	dets := New().Detectors()
	if len(dets) != len(want) {
		t.Fatalf("got %d detectors, want %d", len(dets), len(want))
	}
	for _, d := range dets {
		if want[d.Name()] != d.Protocol() {
			t.Errorf("detector %q protocol %q", d.Name(), d.Protocol())
		}
	}
}

func TestMatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "package.json", `{"dependencies":{"@nestjs/common":"^10.0.0","@nestjs/core":"^10.0.0"}}`)
	writeFile(t, root, "src/cats.controller.ts", "import { Controller, Get } from '@nestjs/common';\n@Controller('cats') export class CatsController {}")
	matched, score := New().Match(root, scan.NewOSFileTree(root, nil))
	if !matched || score != 7 { // ts(1) + package.json @nestjs(3) + import(3)
		t.Fatalf("matched=%v score=%d, want true/7", matched, score)
	}
	// A repo with no .ts must not match.
	javaRepo := t.TempDir()
	writeFile(t, javaRepo, "src/main/java/App.java", "class App {}")
	if m, _ := New().Match(javaRepo, scan.NewOSFileTree(javaRepo, nil)); m {
		t.Error("must not match a non-TS repo")
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

// TestGlobalPrefix locks the setGlobalPrefix('api') composition: every
// controller route gains the app-wide prefix (the "*" MountPrefixes slot).
func TestGlobalPrefix(t *testing.T) {
	sources := map[string]string{
		"src/main.ts": `const app = await NestFactory.create(AppModule);
			app.setGlobalPrefix('api');`,
		"src/tag.controller.ts": `@Controller('tags')
			export class TagController {
				@Get() findAll() {}
			}`,
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
	if err := (globalPrefixIndexer{}).Index(&provider.IndexContext{Parsed: parsed}, idx); err != nil {
		t.Fatalf("index: %v", err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(parsed["src/tag.controller.ts"], []provider.Detector{restDetector{}}, idx, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
	if len(svc.Endpoints) != 1 || svc.Endpoints[0].Path != "/api/tags" {
		t.Errorf("endpoints = %+v, want GET /api/tags", svc.Endpoints)
	}
}

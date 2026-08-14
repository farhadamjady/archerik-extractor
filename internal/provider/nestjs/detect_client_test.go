package nestjs

import (
	"fmt"
	"sort"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/tsjs"
	"github.com/farhadamjady/archerik-extractor/internal/query"
)

func outbound(t *testing.T, src string) []string {
	t.Helper()
	f, err := tsjs.NewParser().Parse("c.ts", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{clientDetector{}}, &provider.Index{}, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
	var out []string
	for _, d := range svc.OutboundDependencies {
		out = append(out, fmt.Sprintf("%s %s %s", d.Detection, d.Confidence, d.TargetName))
	}
	sort.Strings(out)
	return out
}

func TestOutboundClients(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "fetch absolute URL -> confirmed host",
			src:  `async function f() { const r = await fetch('http://payments.svc/charge'); }`,
			want: []string{"fetch confirmed payments.svc"},
		},
		{
			name: "axios.get absolute URL -> confirmed host:port",
			src:  `async function f() { return axios.get('http://catalog:8080/products'); }`,
			want: []string{"axios confirmed catalog:8080"},
		},
		{
			name: "injected httpService.post -> axios confirmed",
			src:  `class S { constructor(private httpService) {} f() { return this.httpService.post('http://orders/place', {}); } }`,
			want: []string{"axios confirmed orders"},
		},
		{
			name: "dynamic fetch URL -> uncertain anonymous edge",
			src:  `async function f(u: string) { await fetch(new URL(u).toString(), { method: 'GET' }); }`,
			want: []string{"fetch uncertain "},
		},
		{
			name: "unrelated .get on a map is not an HTTP client",
			src:  `function f(cache: Map<string, string>) { return cache.get('key'); }`,
			want: nil,
		},
		{
			name: "http client via const-bound literal -> confirmed host",
			src:  `async function f() { const url = 'http://catalog:8080/products'; return axios.get(url); }`,
			want: []string{"axios confirmed catalog:8080"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := outbound(t, tc.src); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

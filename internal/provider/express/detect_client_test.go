package express

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
	f, err := tsjs.NewParser().Parse("c.js", []byte(src))
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
			name: "axios.get absolute URL -> confirmed host",
			src:  `async function f() { return axios.get('http://inventory.svc/items'); }`,
			want: []string{"axios confirmed inventory.svc"},
		},
		{
			name: "fetch absolute URL -> confirmed host:port",
			src:  `async function f() { await fetch('http://auth:9000/verify'); }`,
			want: []string{"fetch confirmed auth:9000"},
		},
		{
			name: "got with dynamic URL -> uncertain",
			src:  `async function f(url) { return got(url); }`,
			want: []string{"axios uncertain "},
		},
		{
			name: "axios.get with a parameter URL -> uncertain",
			src:  `async function f(url) { const r = await axios.get(url, { params }); }`,
			want: []string{"axios uncertain "},
		},
		{
			name: "axios.get via const-bound literal -> confirmed host",
			src:  `async function f() { const url = 'http://pay.svc/charge'; return axios.get(url, { params }); }`,
			want: []string{"axios confirmed pay.svc"},
		},
		{
			name: "per-function const scoping -> no cross-bleed",
			src: `async function a() { const url = 'http://one.svc/x'; return axios.get(url); }
			      async function b() { const url = 'http://two.svc/y'; return axios.get(url); }`,
			want: []string{"axios confirmed one.svc", "axios confirmed two.svc"},
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

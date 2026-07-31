package aspnet

import (
	"fmt"
	"sort"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/csharp"
	"github.com/farhadamjady/service-discovery/internal/query"
)

func outbound(t *testing.T, src string) []string {
	t.Helper()
	f, err := csharp.NewParser().Parse("C.cs", []byte(src))
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

func TestOutboundHttpClient(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "GetAsync absolute URL -> confirmed host",
			src:  `class S { async Task F() { var r = await _http.GetAsync("http://catalog.svc/products"); } }`,
			want: []string{"dotnet-httpclient confirmed catalog.svc"},
		},
		{
			name: "GetFromJsonAsync relative path -> uncertain",
			src:  `class S { async Task F() { var r = await _http.GetFromJsonAsync<Foo>("api/orders/5"); } }`,
			want: []string{"dotnet-httpclient uncertain "},
		},
		{
			name: "bare non-URL string (cache key) is not an HTTP target",
			src:  `class S { async Task F() { var r = await _cache.GetAsync("session-key"); } }`,
			want: nil,
		},
		{
			name: "interpolated URL -> uncertain",
			src:  `class S { async Task F(string id) { var r = await _http.PostAsJsonAsync($"http://x/{id}", body); } }`,
			want: []string{"dotnet-httpclient uncertain "},
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

func TestOutboundRefit(t *testing.T) {
	src := `public interface ICatalogService {
		[Get("/catalog-service/products?pageNumber={pageNumber}")]
		Task<Resp> GetProducts(int pageNumber);
		[Get("/catalog-service/products/{id}")]
		Task<Resp> GetProduct(Guid id);
		[Post("/basket-service/basket")]
		Task<Resp> Store(Basket b);
	}`
	got := outbound(t, src)
	want := []string{
		"refit uncertain basket-service",
		"refit uncertain catalog-service",
		"refit uncertain catalog-service",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

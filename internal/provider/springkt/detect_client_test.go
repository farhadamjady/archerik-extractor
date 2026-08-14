package springkt

import (
	"fmt"
	"sort"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/kotlin"
	"github.com/farhadamjady/archerik-extractor/internal/query"
)

func outbound(t *testing.T, src string) []string {
	t.Helper()
	f, err := kotlin.NewParser().Parse("C.kt", []byte(src))
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
			name: "FeignClient with name -> confirmed logical name",
			src: `@FeignClient(name = "payment-service", url = "http://localhost:8080")
				interface PaymentClient { @GetMapping("/pay") fun pay(): String }`,
			want: []string{"feign confirmed payment-service"},
		},
		{
			name: "FeignClient multi-line args",
			src: `@FeignClient(
					name = "stores",
					url = "http://localhost:9090"
				)
				interface StoreClient { @GetMapping("/stores") fun stores(): List<Store> }`,
			want: []string{"feign confirmed stores"},
		},
		{
			name: "FeignClient url only -> host authority",
			src: `@FeignClient(url = "http://inventory:8081")
				interface InventoryClient { @GetMapping("/i") fun i(): String }`,
			want: []string{"feign confirmed inventory:8081"},
		},
		{
			name: "RestTemplate getForObject absolute URL -> confirmed host",
			src: `class S(val restTemplate: RestTemplate) {
					fun f() = restTemplate.getForObject("http://catalog.svc/products", String::class.java)
				}`,
			want: []string{"resttemplate confirmed catalog.svc"},
		},
		{
			name: "plain class without Feign/RestTemplate -> nothing",
			src:  `class Foo { fun bar() = listOf(1,2,3).get(0) }`,
			want: nil,
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

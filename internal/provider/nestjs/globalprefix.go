package nestjs

import (
	"sort"

	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
)

// globalPrefixIndexer finds NestJS's app-wide route prefix —
// `app.setGlobalPrefix('api')` in main.ts — and stores it under the reserved
// "*" key of Index.MountPrefixes (the per-file keys are Express's #50 slot; the
// star key is the applies-to-every-controller slot). Without it every emitted
// path is missing its leading segment (`/users` instead of `/api/users`).
// `setGlobalPrefix(v, {exclude: ...})` exclusions are not modeled (rare; the
// excluded routes would gain a prefix they don't have — accepted round-2 bound).
type globalPrefixIndexer struct{}

func (globalPrefixIndexer) Name() string { return "nestjs.global-prefix" }

func (globalPrefixIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	seen := map[string]bool{}
	var files []string
	for p, pf := range ic.Parsed {
		if _, ok := pf.(*tsjs.File); ok {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	for _, p := range files {
		jf := ic.Parsed[p].(*tsjs.File)
		jf.Root().Walk(func(n tsjs.Node) bool {
			if n.Type() != "call_expression" {
				return true
			}
			fn := n.ChildByFieldName("function")
			if fn.Type() != "member_expression" || fn.ChildByFieldName("property").Text() != "setGlobalPrefix" {
				return true
			}
			args := tsjs.NamedChildren(n.ChildByFieldName("arguments"))
			if len(args) >= 1 && args[0].Type() == "string" {
				if v := tsjs.StringValue(args[0]); v != "" {
					seen["/"+v] = true
				}
			}
			return true
		})
	}
	if len(seen) == 0 {
		return nil
	}
	var prefixes []string
	for p := range seen {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	if idx.MountPrefixes == nil {
		idx.MountPrefixes = map[string][]string{}
	}
	idx.MountPrefixes["*"] = prefixes
	return nil
}

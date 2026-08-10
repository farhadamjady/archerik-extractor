package nethttp

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/golang"
)

// clientDetector extracts outbound HTTP dependencies from std-lib client calls:
// `http.Get(url)` / `http.Post(url, ct, body)` / `http.Head` / `http.PostForm`
// and `http.NewRequest("GET", url, body)` / `http.NewRequestWithContext(...)`.
// A literal absolute URL resolves to its authority (host[:port]) as the
// target_name (path variants of one service share a node, like the JVM
// providers); a dynamic URL emits an honest anonymous uncertain edge — never
// dropped (the honesty rule). Calls on an *http.Client receiver
// (`client.Get(...)`) need type tracking and are a documented later step; this
// round covers the package-level `http.` calls.
type clientDetector struct{}

func (clientDetector) Name() string             { return "nethttp.client" }
func (clientDetector) Protocol() model.Protocol { return model.ProtoREST }

// clientQuery mirrors routeQuery's shape; the handler filters to http.<verb>.
const clientQuery = `(call_expression
  function: (selector_expression
    operand: (identifier) @recv
    field: (field_identifier) @field)
  arguments: (argument_list) @args
) @call`

func (d clientDetector) Rules() []provider.Rule {
	return []provider.Rule{{Query: clientQuery, OnMatch: d.onCall}}
}

// urlArgIndex maps the http.* call to the position of its URL argument.
var urlArgIndex = map[string]int{
	"Get": 0, "Post": 0, "Head": 0, "PostForm": 0,
	"NewRequest": 1, "NewRequestWithContext": 2,
}

func (clientDetector) onCall(mc *provider.MatchContext) {
	recv, _ := mc.Captures["recv"].(golang.Node)
	field, _ := mc.Captures["field"].(golang.Node)
	args, _ := mc.Captures["args"].(golang.Node)
	if !recv.Valid() || recv.Text() != "http" {
		return
	}
	idx, isClient := urlArgIndex[field.Text()]
	if !isClient {
		return
	}
	if jf, ok := mc.File.(*golang.File); !ok || !strings.Contains(string(jf.Src()), `"net/http"`) {
		return
	}
	kids := golang.NamedChildren(args)
	if len(kids) <= idx {
		return
	}
	base := model.Dependency{Protocol: model.ProtoREST, Detection: model.DetectHTTPClient}
	url, isLit := golang.StringLit(kids[idx])
	switch {
	case isLit && strings.Contains(url, "://"):
		if host := authority(url); host != "" {
			base.TargetName, base.URL, base.Resolved, base.Confidence = host, url, true, model.Confirmed
		} else {
			base.URL, base.Confidence = url, model.Uncertain
		}
	case isLit:
		// A bare path / relative URL — this call site names no service.
		base.URL, base.Confidence = url, model.Uncertain
	default:
		// Dynamic URL (variable/concat/fmt.Sprintf) — a Go value evaluator is a
		// later round; emit the honest anonymous uncertain edge.
		base.Confidence = model.Uncertain
	}
	mc.Out.OutboundDependencies = append(mc.Out.OutboundDependencies, base)
}

// authority returns host[:port] of an absolute URL, or "" when empty.
func authority(url string) string {
	i := strings.Index(url, "://")
	if i < 0 {
		return ""
	}
	a := url[i+3:]
	if j := strings.IndexByte(a, '/'); j >= 0 {
		a = a[:j]
	}
	return a
}

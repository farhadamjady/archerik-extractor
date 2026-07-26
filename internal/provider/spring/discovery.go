package spring

import (
	"strings"

	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
	"github.com/farhadamjady/service-discovery/internal/resolve"
)

// discoveryTarget resolves the logical service name behind a service-registry
// lookup that an outbound URL is built from (IMPROVEMENTS #37). Netflix/Spring
// code routinely asks Eureka for a live instance and assembles the URL from its
// RUNTIME host/port:
//
//	InstanceInfo i = eurekaClient.getApplication(appName).getInstances()...get();
//	String url = "http://" + i.getHostName() + ":" + i.getPort() + "/profile";
//	restTemplate.exchange(url, ...);
//
// The host is a runtime value (so the URL alone yields only an anonymous
// uncertain edge), but the TARGET is statically known: the argument to
// getApplication / getNextServerFromEureka. We locate that call in the same
// method as the URL expression and resolve its argument through the value+config
// layer (here a `@Value("${com.amdocs.external.application.name}")` field →
// `PROFILE-SERVICE`), returning the logical name for the backend to map — the
// same "emit the raw name" contract as a service-discovery @FeignClient.
//
// Capped confidence is the caller's concern (registry indirection = likely).
func discoveryTarget(mc *provider.MatchContext, urlExpr java.Node) (name, source string, ok bool) {
	if mc.Resolver == nil {
		return "", "", false
	}
	method := enclosingMethod(urlExpr)
	if !method.Valid() {
		return "", "", false
	}
	// getApplication is a generic name; gate it on the file actually using the
	// Netflix discovery client. getNextServerFromEureka is unambiguous on its own.
	eureka := fileImportsEureka(mc.File)

	var found bool
	method.Walk(func(n java.Node) bool {
		if found || n.Type() != "method_invocation" {
			return true
		}
		m := n.ChildByFieldName("name").Text()
		if m != "getNextServerFromEureka" && !(m == "getApplication" && eureka) {
			return true
		}
		arg := n.ChildByFieldName("arguments").NamedChild(0)
		if !arg.Valid() {
			return true
		}
		if v, src, isName := singleName(mc.Resolver.Resolve(arg)); isName {
			name, source, found = v, src, true
			return false // stop the walk
		}
		return true
	})
	return name, source, found
}

// enclosingMethod returns the method/constructor/lambda declaration containing n,
// or an invalid Node if none — the scope we search for the registry lookup so a
// getApplication call in an unrelated method never bleeds in.
func enclosingMethod(n java.Node) java.Node {
	for p := n.Parent(); p.Valid(); p = p.Parent() {
		switch p.Type() {
		case "method_declaration", "constructor_declaration", "lambda_expression":
			return p
		}
	}
	return java.Node{}
}

// singleName returns the one resolved value of vs when it is a single exact
// logical name (a host/service label — not a URL or a bare path), plus its
// config provenance, else ok=false.
func singleName(vs resolve.ValueSet) (name, source string, ok bool) {
	if vs.Kind != resolve.Exact || len(vs.Values) != 1 {
		return "", "", false
	}
	host, ok := targetHost(vs.Values[0].S)
	return host, vs.Values[0].Source, ok
}

// fileImportsEureka reports whether the file references the Netflix Eureka
// discovery client — the gate for the otherwise-generic getApplication name.
func fileImportsEureka(f provider.ParsedFile) bool {
	jf, ok := f.(*java.File)
	if !ok {
		return false
	}
	return strings.Contains(string(jf.Src()), "com.netflix.discovery")
}

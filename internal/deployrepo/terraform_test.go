package deployrepo

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/scan"
)

func TestParseTerraformLiteralName(t *testing.T) {
	src := []byte(`
module "service_a" {
  source = "./modules/service"
  name   = "service-a"
  # a nested block must not confuse attribute lookup
  scaling {
    name = "should-be-ignored"
    min  = 1
  }
}
`)
	entries, err := parseTerraformModules("service-a.tf", src, ResolverOptions{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want 1", entries)
	}
	e := entries[0]
	if e.ServiceName != "service-a" {
		t.Errorf("ServiceName = %q, want service-a (module input, not the nested block)", e.ServiceName)
	}
	if e.Source != model.SourceTerraform || e.Confidence != model.IdentityLikely {
		t.Errorf("source/confidence = %v/%v, want terraform/likely", e.Source, e.Confidence)
	}
	// bare service name is emitted as an in-cluster host (the reliable join key)
	if vals := model.HostValues(e.Hosts); !contains(vals, "service-a") {
		t.Errorf("hosts = %v, want to include bare service-a", vals)
	}
	for _, h := range e.Hosts {
		if h.Resolver != model.SourceTerraform {
			t.Errorf("host %q resolver = %q, want terraform", h.Value, h.Resolver)
		}
	}
}

func TestParseTerraformSkipsNonLiteralName(t *testing.T) {
	src := []byte(`
module "service_b" {
  source = "./modules/service"
  name   = var.service_name   # not a literal -> skipped, never guessed
}
module "service_c" {
  source = "./modules/service"
  # no name at all
}
`)
	entries, err := parseTerraformModules("x.tf", src, ResolverOptions{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %+v, want none (non-literal + missing name)", entries)
	}
}

func TestParseTerraformNamespaceAttrAndConvention(t *testing.T) {
	withNS := []byte("module \"a\" {\n  name      = \"svc-a\"\n  namespace = \"team-a\"\n}\n")
	entries, err := parseTerraformModules("a.tf", withNS, ResolverOptions{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 1 || entries[0].Namespace != "team-a" {
		t.Fatalf("declared namespace not used: %+v", entries)
	}

	noNS := []byte("module \"a\" {\n  name = \"svc-a\"\n}\n")
	entries, err = parseTerraformModules("a.tf", noNS, ResolverOptions{NamespaceConvention: "service-name"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 1 || entries[0].Namespace != "svc-a" {
		t.Fatalf("namespace convention not applied: %+v", entries)
	}
}

func TestTerraformResolverIsOptIn(t *testing.T) {
	// Not in the default set.
	rs, _, err := Select(nil, ResolverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if resolverNames(rs)["terraform"] {
		t.Error("terraform must NOT run by default")
	}
	// Selectable explicitly.
	rs, _, err = Select([]string{"terraform"}, ResolverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if names := resolverNames(rs); len(names) != 1 || !names["terraform"] {
		t.Errorf("resolvers = %v, want only terraform", names)
	}
}

func TestTerraformResolverEndToEnd(t *testing.T) {
	root := t.TempDir()
	write(t, root, "service-a.tf", `module "service_a" {
  source = "./modules/service"
  name   = "service-a"
}`)
	tree := scan.NewOSFileTree(root, nil)
	entries, errs := terraformResolver{}.Resolve(ResolveContext{AbsRoot: root, Tree: tree})
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(entries) != 1 || entries[0].ServiceName != "service-a" {
		t.Fatalf("entries = %+v", entries)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

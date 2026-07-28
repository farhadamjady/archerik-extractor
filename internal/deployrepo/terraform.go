package deployrepo

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/farhadamjady/service-discovery/internal/model"
)

// terraformResolver reads Terraform (*.tf) module blocks and extracts the
// service name declared as a literal `name = "..."` input — the identity many
// orgs assign in their infra repo, distinct from the chart/manifest they
// deploy. It is LITERAL-ONLY and honest: a `name` that references a variable
// (var.x), a local, or any interpolation is skipped, never guessed. It does
// not evaluate variables, trace modules, or read *.tfvars.
//
// Opt-in (not in the default resolver set): a TF repo is a different repo than
// the GitOps repo, and vendored third-party modules would otherwise inject
// their own `name`s as noise into a normal deploy-repo scan. Enable with
// --resolvers=terraform (alone or combined).
//
// Confidence is `likely`, not `confirmed`: the name is a confirmed literal, but
// the host is INFERRED from it (no rendered Service was seen). The bare service
// name is the reliable join key; the namespace-qualified host forms are
// best-effort (namespace defaults, or --namespace-convention).
type terraformResolver struct{}

func (terraformResolver) Name() string { return "terraform" }

func (terraformResolver) Resolve(rc ResolveContext) ([]model.IdentityEntry, []RenderError) {
	var entries []model.IdentityEntry
	var errs []RenderError
	seen := map[string]bool{}
	for _, rel := range rc.Tree.Glob("**/*.tf") {
		if seen[rel] {
			continue
		}
		seen[rel] = true
		src, err := rc.Tree.Read(rel)
		if err != nil {
			errs = append(errs, RenderError{Unit: rel, Err: err})
			continue
		}
		e, perr := parseTerraformModules(rel, src, rc.Opts)
		if perr != nil {
			errs = append(errs, RenderError{Unit: rel, Err: perr})
		}
		entries = append(entries, e...)
	}
	return entries, errs
}

// parseTerraformModules extracts one identity entry per top-level module block
// carrying a literal `name`. An optional literal `namespace` attribute is used
// when present; otherwise the namespace defaults (or follows the convention).
func parseTerraformModules(path string, src []byte, opts ResolverOptions) ([]model.IdentityEntry, error) {
	f, diags := hclsyntax.ParseConfig(src, path, hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse hcl: %s", diags.Error())
	}
	body, ok := f.Body.(*hclsyntax.Body)
	if !ok {
		return nil, nil
	}

	var out []model.IdentityEntry
	for _, blk := range body.Blocks {
		if blk.Type != "module" {
			continue
		}
		name := literalStringAttr(blk.Body, "name")
		if name == "" {
			continue // absent or non-literal (var/interpolation) — skipped, never guessed
		}
		ns := namespaceOrDefault(literalStringAttr(blk.Body, "namespace"), name, opts)
		out = append(out, model.IdentityEntry{
			ServiceName: name,
			Namespace:   ns,
			Source:      model.SourceTerraform,
			Confidence:  model.IdentityLikely,
			Hosts:       serviceHosts(name, ns, model.SourceTerraform),
		})
	}
	return out, nil
}

// literalStringAttr returns an attribute's value only when it is a literal
// string. Evaluating with a nil EvalContext succeeds for a plain string but
// errors on any variable/local reference, which we treat as "not a literal"
// and skip — the honesty boundary.
func literalStringAttr(body *hclsyntax.Body, name string) string {
	attr, ok := body.Attributes[name]
	if !ok {
		return ""
	}
	v, diags := attr.Expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || v.Type() != cty.String {
		return ""
	}
	return v.AsString()
}

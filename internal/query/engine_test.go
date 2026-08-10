package query

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/java"
)

const src = `package com.acme;
@RestController
public class UserController {
    public String hello() { return "hi"; }
}
class Helper {}
`

// det is a throwaway detector carrying arbitrary rules for the engine tests.
type det struct{ rules []provider.Rule }

func (det) Name() string             { return "test" }
func (det) Protocol() model.Protocol { return model.ProtoREST }
func (d det) Rules() []provider.Rule { return d.rules }

func parse(t *testing.T) provider.ParsedFile {
	t.Helper()
	f, err := java.NewParser().Parse("UserController.java", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return f
}

// text pulls a capture's source text, asserting it exists.
func text(t *testing.T, caps map[string]provider.ASTNode, name string) string {
	t.Helper()
	n, ok := caps[name].(java.Node)
	if !ok || !n.Valid() {
		t.Fatalf("capture %q missing", name)
	}
	return n.Text()
}

// TestDispatchAndCaptures is the smoke test: a class-name rule fires once
// per class, in source order, with the right captured text.
func TestDispatchAndCaptures(t *testing.T) {
	var got []string
	d := det{rules: []provider.Rule{{
		Query: `(class_declaration name: (identifier) @cls)`,
		OnMatch: func(mc *provider.MatchContext) {
			got = append(got, text(t, mc.Captures, "cls"))
		},
	}}}

	if err := New().Run(parse(t), []provider.Detector{d}, &provider.Index{}, nil, model.NewService("s", "s", "")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 2 || got[0] != "UserController" || got[1] != "Helper" {
		t.Errorf("captured classes = %v, want [UserController Helper]", got)
	}
}

// TestMultiRuleRouting proves each match reaches ITS rule's handler: two rules
// (across two detectors) with distinct captures must not cross-fire.
func TestMultiRuleRouting(t *testing.T) {
	var classes, methods []string
	classRule := det{rules: []provider.Rule{{
		Query:   `(class_declaration name: (identifier) @cls)`,
		OnMatch: func(mc *provider.MatchContext) { classes = append(classes, text(t, mc.Captures, "cls")) },
	}}}
	methodRule := det{rules: []provider.Rule{{
		Query:   `(method_declaration name: (identifier) @m)`,
		OnMatch: func(mc *provider.MatchContext) { methods = append(methods, text(t, mc.Captures, "m")) },
	}}}

	err := New().Run(parse(t), []provider.Detector{classRule, methodRule}, &provider.Index{}, nil, model.NewService("s", "s", ""))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(classes) != 2 {
		t.Errorf("classes = %v, want 2", classes)
	}
	if len(methods) != 1 || methods[0] != "hello" {
		t.Errorf("methods = %v, want [hello]", methods)
	}
}

// TestNonQueryableSkipped: a parsed file that is not a QueryRunner (config /
// schema kinds) is silently skipped, not an error.
func TestNonQueryableSkipped(t *testing.T) {
	fired := false
	d := det{rules: []provider.Rule{{
		Query:   `(class_declaration) @c`,
		OnMatch: func(*provider.MatchContext) { fired = true },
	}}}
	if err := New().Run(notQueryable{}, []provider.Detector{d}, &provider.Index{}, nil, nil); err != nil {
		t.Fatalf("Run over non-queryable file: %v", err)
	}
	if fired {
		t.Error("handler fired on a non-queryable file")
	}
}

// TestInvalidQuerySurfaces: a malformed pattern is a hard error, not a silent
// miss — detector authors get told.
func TestInvalidQuerySurfaces(t *testing.T) {
	d := det{rules: []provider.Rule{{Query: `(class_declaration`, OnMatch: func(*provider.MatchContext) {}}}}
	if err := New().Run(parse(t), []provider.Detector{d}, &provider.Index{}, nil, nil); err == nil {
		t.Fatal("expected error from malformed query, got nil")
	}
}

// TestNoRulesNoOp: a detector declaring zero rules is a clean no-op.
func TestNoRulesNoOp(t *testing.T) {
	if err := New().Run(parse(t), []provider.Detector{det{}}, &provider.Index{}, nil, nil); err != nil {
		t.Fatalf("Run with no rules: %v", err)
	}
}

// notQueryable is a ParsedFile that does not implement provider.QueryRunner.
type notQueryable struct{}

func (notQueryable) Path() string            { return "application.yml" }
func (notQueryable) Kind() provider.FileKind { return provider.KindSpringConfig }

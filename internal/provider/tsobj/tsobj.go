// Package tsobj reads a JavaScript/TypeScript object literal as a response
// contract. Both Node providers need it and for the same reason: a handler that
// builds its wire object inline — `return { data: profile, total }` in NestJS,
// `res.json({ data, total })` in Express — states the response shape in a place
// no declared type covers. The literal IS the body, so its KEYS are the
// contract; they are read straight off the AST and cost nothing to trust.
//
// What differs between the two providers is how a VALUE is chased back to a
// declared type (NestJS has controller fields and a method-return index; Express
// has local variable types). That part is injected, so the structure — key
// reading, confidence rules, nesting, truncation — lives here once.
package tsobj

import (
	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/tsjs"
)

// Resolve chases a value expression to a declared type NAME, or "" when the
// trail runs out. Provider-specific.
type Resolve func(expr tsjs.Node) string

// TypeSchema turns a type name into a schema, or nil when it resolves to
// nothing. Provider-specific: each applies its own alias expansion, walker and
// confidence cap.
type TypeSchema func(typeName string) *model.Schema

// Schema reads an object literal as a response body.
//
// The object itself is `confirmed` — its keys are written in the source. Values
// carry their own confidence: an inline literal is `confirmed`, a value chased
// to a declared type is `likely` (it took an indirection to get there), and an
// expression that resolves to nothing is an `uncertain` object that KEEPS ITS
// NAME. Dropping it would be a lie by omission; a named field of unknown shape
// is a real contract.
//
// depth is the nesting budget for literals inside literals; at zero the inner
// object becomes the standard truncation boundary.
func Schema(obj tsjs.Node, depth int, resolve Resolve, typeSchema TypeSchema) *model.Schema {
	if depth <= 0 {
		return &model.Schema{Type: "object", Required: model.ReqUnknown, Truncated: true, Confidence: model.Confirmed}
	}
	s := &model.Schema{Type: "object", Required: model.ReqUnknown, Confidence: model.Confirmed}
	for _, p := range tsjs.NamedChildren(obj) {
		switch p.Type() {
		case "pair":
			name, ok := literalKey(p.ChildByFieldName("key"))
			if !ok {
				// A computed key (`{ [k]: v }`) means the key set isn't static.
				s.Confidence = model.Uncertain
				continue
			}
			s.Nested = append(s.Nested, valueSchema(name, p.ChildByFieldName("value"), depth, resolve, typeSchema))
		case "shorthand_property_identifier":
			// `{ data }` — the key names its own value.
			s.Nested = append(s.Nested, valueSchema(p.Text(), p, depth, resolve, typeSchema))
		case "spread_element":
			// `{ ...rest }` merges in keys we cannot enumerate statically, so the
			// field list is no longer known to be complete.
			s.Confidence = model.Uncertain
		}
	}
	return s
}

// literalKey returns a property key's name when it is statically known — a bare
// identifier (`data:`), a quoted key (`'data':`), or a numeric key. A computed
// key is not, and reports ok=false.
func literalKey(key tsjs.Node) (string, bool) {
	if !key.Valid() {
		return "", false
	}
	switch key.Type() {
	case "property_identifier":
		return key.Text(), true
	case "string":
		return tsjs.StringValue(key), true
	case "number":
		return key.Text(), true
	}
	return "", false
}

// valueSchema types one key of the literal.
func valueSchema(name string, val tsjs.Node, depth int, resolve Resolve, typeSchema TypeSchema) model.Schema {
	if s, ok := literalValueSchema(val); ok {
		s.Name = name
		s.Required = model.ReqUnknown
		return s
	}
	if val.Valid() && val.Type() == "object" {
		s := Schema(val, depth-1, resolve, typeSchema)
		s.Name = name
		return *s
	}
	if t := resolve(val); t != "" {
		if s := typeSchema(t); s != nil {
			s.Name = name
			// Reached through the code rather than declared at this position, so a
			// resolved type is `likely`, not confirmed. A type that resolved to
			// nothing (`any` -> opaque object) stays uncertain — being chased to a
			// declaration that says nothing is not evidence, and must not read as
			// though it were.
			if s.Confidence == model.Confirmed {
				s.Confidence = model.Likely
			}
			return *s
		}
	}
	return model.Schema{Name: name, Type: "object", Required: model.ReqUnknown, Confidence: model.Uncertain}
}

// literalValueSchema types an inline literal value — the one case where the
// source states the type outright.
func literalValueSchema(val tsjs.Node) (model.Schema, bool) {
	if !val.Valid() {
		return model.Schema{}, false
	}
	switch val.Type() {
	case "string", "template_string":
		return model.Schema{Type: "string", Confidence: model.Confirmed}, true
	case "number":
		return model.Schema{Type: "number", Confidence: model.Confirmed}, true
	case "true", "false":
		return model.Schema{Type: "boolean", Confidence: model.Confirmed}, true
	case "null", "undefined":
		// A literal null names the field but says nothing about its type.
		return model.Schema{Type: "object", Nullable: true, Confidence: model.Uncertain}, true
	}
	return model.Schema{}, false
}

// ReturnedObject returns the object literal a `return` statement yields,
// unwrapping a leading `await`. An invalid node when the return isn't a literal.
func ReturnedObject(ret tsjs.Node) tsjs.Node {
	for _, c := range tsjs.NamedChildren(ret) {
		switch c.Type() {
		case "await_expression", "parenthesized_expression":
			for _, cc := range tsjs.NamedChildren(c) {
				if cc.Type() == "object" {
					return cc
				}
			}
		case "object":
			return c
		}
	}
	return tsjs.Node{}
}

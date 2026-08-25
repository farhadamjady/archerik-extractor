package laravel

import (
	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/php"
	"github.com/farhadamjady/archerik-extractor/internal/schema"
)

// arraySchema reads a PHP array literal as the object it serializes to.
//
// This is the primary schema source for Laravel, not a fallback. A PHP handler
// almost never declares a return type, and the framework's own idioms —
// `response()->json([...])`, a JsonResource's `toArray()` — put the wire shape
// in an array literal. The keys are literally in the source, which makes them
// the strongest evidence available (docs/CROSS-STACK-CHECKS.md, Check 1).
//
// depth is the remaining nesting budget. The literal IS the root of the body, so
// a caller passes the walker's FULL depth; a named type reached THROUGH one of
// its keys already sits a level down and is walked with a nested budget.
func arraySchema(arr php.Node, depth int, res *resolver) *model.Schema {
	if !arr.Valid() || arr.Type() != "array_creation_expression" {
		return nil
	}
	entries := php.ArrayEntries(arr)
	out := &model.Schema{Type: "object", Required: model.ReqUnknown, Confidence: model.Confirmed}
	if depth <= 0 {
		out.Truncated = true
		return out
	}
	for _, e := range entries {
		if e.Key == "" {
			// A positional entry means this is a LIST, not an object. Its shape
			// is the element shape, and the key list we would otherwise report
			// would be a lie about the payload.
			return listSchema(entries, depth, res)
		}
		f := valueSchema(e.Value, depth-1, res)
		f.Name = e.Key
		out.Nested = append(out.Nested, *f)
	}
	if len(out.Nested) == 0 {
		// `[]` json_encodes to an ARRAY, not an object, and it names no element
		// shape. Real, but nothing is known about what goes in it.
		out.Type = "array"
		out.Confidence = model.Uncertain
	}
	return out
}

// listSchema describes a positional array: a JSON array whose element shape is
// taken from the first entry that has one.
func listSchema(entries []php.ArrayEntry, depth int, res *resolver) *model.Schema {
	out := &model.Schema{Type: "array", Required: model.ReqUnknown, Confidence: model.Confirmed}
	for _, e := range entries {
		if s := valueSchema(e.Value, depth-1, res); s.Confidence != model.Uncertain {
			out.Nested = s.Nested
			out.Items = s.Type
			return out
		}
	}
	out.Confidence = model.Uncertain
	return out
}

// valueSchema types one array value. A value it cannot type still yields a
// schema — the field NAME is a real part of the contract even when the shape
// behind it is not knowable, and dropping it would understate the payload.
func valueSchema(v php.Node, depth int, res *resolver) *model.Schema {
	unknown := &model.Schema{Type: "object", Required: model.ReqUnknown, Confidence: model.Uncertain}
	if !v.Valid() {
		return unknown
	}
	switch v.Type() {
	case "string", "encapsed_string":
		return &model.Schema{Type: "string", Required: model.ReqUnknown, Confidence: model.Confirmed}
	case "integer":
		return &model.Schema{Type: "int", Required: model.ReqUnknown, Confidence: model.Confirmed}
	case "float":
		return &model.Schema{Type: "float", Required: model.ReqUnknown, Confidence: model.Confirmed}
	case "boolean":
		return &model.Schema{Type: "bool", Required: model.ReqUnknown, Confidence: model.Confirmed}
	case "null":
		return &model.Schema{Type: "object", Nullable: true, Required: model.ReqUnknown, Confidence: model.Confirmed}
	case "array_creation_expression":
		if s := arraySchema(v, depth, res); s != nil {
			return s
		}
		return unknown
	case "object_creation_expression":
		// `new ProfileResource($x)` nested inside a payload is that resource's
		// shape — one indirection, so `likely` rather than `confirmed`.
		if s := res.resourceSchema(v, depth); s != nil {
			return s
		}
		return unknown
	}
	if php.IsCall(v) {
		if s := res.callSchema(v, depth); s != nil {
			return s
		}
	}
	return unknown
}

// resolver carries what array reading needs from the rest of the index: the
// class index for nested resources, and the walker's depth budgeting so
// truncation lands where an equivalent declared body's would.
type resolver struct {
	idx    *provider.Index
	walker *schema.Walker
	// seen guards against a resource cycle (A embeds B embeds A), which is legal
	// to write because the recursion is broken at runtime by the data.
	seen map[string]bool
}

func newResolver(idx *provider.Index) *resolver {
	depth := 0
	if idx != nil {
		depth = idx.SchemaDepth
	}
	return &resolver{idx: idx, walker: schema.NewWalkerDepth(nil, depth), seen: map[string]bool{}}
}

// resourceSchema resolves `new SomeResource($x)` to the shape SomeResource's
// toArray() returns. A resource that is not a JsonResource — a DTO, a value
// object — is not a payload shape this can read.
func (r *resolver) resourceSchema(newExpr php.Node, depth int) *model.Schema {
	if newExpr.NamedChildCount() == 0 {
		return nil
	}
	return r.byClassName(newExpr, newExpr.NamedChild(0).Text(), depth)
}

// byClassName resolves a resource class to its payload shape, unwrapped: the
// `$wrap` key belongs to the top-level response only, and Laravel drops it for a
// nested resource.
func (r *resolver) byClassName(ctx php.Node, name string, depth int) *model.Schema {
	cls, ok := lookupClass(r.idx, ctx, name)
	if !ok || !cls.extends("JsonResource") && !cls.extends("ResourceCollection") {
		return nil
	}
	if r.seen[cls.name()] || depth <= 0 {
		return &model.Schema{Type: cls.name(), Truncated: true, Required: model.ReqUnknown, Confidence: model.Likely}
	}
	r.seen[cls.name()] = true
	defer delete(r.seen, cls.name())

	// Name the shape by the class's OWN declared name, not by the reference that
	// reached it. laravel-blog imports the same class as `PostResource` in one
	// file and `Post` in another; keying on the local alias would split one
	// shape into two types in the graph.
	declared := cls.name()
	if declared == "" {
		declared = simpleClassName(name)
	}
	if m, ok := cls.method("toArray"); ok {
		if s := r.methodPayload(m, depth); s != nil {
			s.Type = declared
			s.Confidence = model.Likely // reached through a class reference
			return s
		}
	}
	return r.collectionSchema(ctx, cls, declared, depth)
}

// collectionSchema handles a ResourceCollection, which usually declares no
// toArray() at all. Its payload is a LIST, and the element shape is named by the
// `$collects` property:
//
//	class ArticlesCollection extends ResourceCollection {
//	    public $collects = ArticleResource::class;
//	}
//
// Without this the whole collection endpoint reports no response — and list
// endpoints are a large share of any API.
func (r *resolver) collectionSchema(ctx php.Node, cls phpClass, name string, depth int) *model.Schema {
	if !cls.extends("ResourceCollection") {
		return nil
	}
	out := &model.Schema{Type: "array", Required: model.ReqUnknown, Confidence: model.Likely}
	elem, ok := cls.propertyExpr("collects")
	if !ok {
		// A collection whose element type is not declared is still a list; the
		// element shape is simply unknown.
		out.Confidence = model.Uncertain
		return out
	}
	if s := r.byClassName(cls.node, elem, depth-1); s != nil {
		out.Items = s.Type
		out.Nested = s.Nested
	} else {
		out.Items = simpleClassName(elem)
	}
	return out
}

// methodPayload reads the array a toArray()-style method returns, including the
// `array_merge(parent::toArray($r), [...])` form Laravel resources use to extend
// a base resource's fields.
func (r *resolver) methodPayload(method php.Node, depth int) *model.Schema {
	ret := returnedExpr(method)
	if !ret.Valid() {
		return nil
	}
	if ret.Type() == "array_creation_expression" {
		return arraySchema(ret, depth, r)
	}
	if php.IsCall(ret) && php.CallName(ret) == "array_merge" {
		return r.mergedPayload(ret, depth)
	}
	return nil
}

// mergedPayload combines the arguments of `array_merge(...)`. A part it cannot
// read makes the RESULT incomplete, so the object is marked uncertain rather
// than presenting the readable half as the whole contract.
func (r *resolver) mergedPayload(call php.Node, depth int) *model.Schema {
	out := &model.Schema{Type: "object", Required: model.ReqUnknown, Confidence: model.Confirmed}
	complete := true
	for _, a := range php.CallArgs(call) {
		var part *model.Schema
		switch {
		case a.Value.Type() == "array_creation_expression":
			part = arraySchema(a.Value, depth, r)
		case php.IsCall(a.Value):
			part = r.parentPayload(a.Value, depth)
		}
		if part == nil {
			complete = false
			continue
		}
		out.Nested = append(out.Nested, part.Nested...)
	}
	if !complete || len(out.Nested) == 0 {
		out.Confidence = model.Uncertain
	}
	return out
}

// parentPayload reads `parent::toArray($request)` — the base resource's fields.
func (r *resolver) parentPayload(call php.Node, depth int) *model.Schema {
	m, ok := r.parentMethod(call, "toArray")
	if !ok {
		return nil
	}
	return r.methodPayload(m, depth)
}

// parentMethod resolves `parent::<name>(...)` to the method it calls, by finding
// the class enclosing the call and following its `extends`. Both a resource's
// toArray() and a FormRequest's rules() extend a base class this way.
func (r *resolver) parentMethod(call php.Node, name string) (php.Node, bool) {
	if call.Type() != "scoped_call_expression" || php.CallName(call) != name {
		return php.Node{}, false
	}
	if php.CallReceiver(call).Text() != "parent" {
		return php.Node{}, false
	}
	cls, ok := lookupClass(r.idx, call, enclosingClassName(call))
	if !ok {
		return php.Node{}, false
	}
	parent, ok := lookupClass(r.idx, cls.node, cls.parentName())
	if !ok {
		return php.Node{}, false
	}
	return parent.method(name)
}

// staticCollectionSchema reads `UserResource::collection($users)` — the factory
// every JsonResource inherits, and the idiomatic way to return a list without
// declaring a ResourceCollection class. The payload is an array of that
// resource.
func (r *resolver) staticCollectionSchema(call php.Node, depth int) *model.Schema {
	if call.Type() != "scoped_call_expression" {
		return nil
	}
	switch php.CallName(call) {
	case "collection", "make":
	default:
		return nil
	}
	ref := php.CallReceiver(call).Text()
	elem := r.byClassName(call, ref, depth-1)
	if elem == nil {
		return nil
	}
	if php.CallName(call) == "make" {
		return elem // make() builds ONE resource, not a list
	}
	out := &model.Schema{Type: "array", Items: elem.Type, Required: model.ReqUnknown, Confidence: model.Likely}
	out.Nested = elem.Nested
	return r.wrapByName(call, ref, out)
}

// wrapByName applies a resource class's $wrap key to a payload.
func (r *resolver) wrapByName(ctx php.Node, name string, inner *model.Schema) *model.Schema {
	cls, ok := lookupClass(r.idx, ctx, name)
	if !ok {
		return inner
	}
	key, ok := cls.staticString("wrap")
	if !ok || key == "" {
		return inner
	}
	field := *inner
	field.Name = key
	return &model.Schema{
		Type: "object", Required: model.ReqUnknown, Confidence: inner.Confidence,
		Nested: []model.Schema{field},
	}
}

// callSchema types a call appearing as a payload value. Only the framework
// helpers whose shape is knowable are read.
func (r *resolver) callSchema(call php.Node, depth int) *model.Schema {
	switch php.CallName(call) {
	case "collect":
		return &model.Schema{Type: "array", Required: model.ReqUnknown, Confidence: model.Likely}
	case "count":
		return &model.Schema{Type: "int", Required: model.ReqUnknown, Confidence: model.Likely}
	}
	return nil
}

// enclosingClassName returns the simple name of the class a node sits in.
func enclosingClassName(n php.Node) string {
	for p := n.Parent(); p.Valid(); p = p.Parent() {
		if p.Type() == "class_declaration" {
			return php.ChildByType(p, "name").Text()
		}
	}
	return ""
}

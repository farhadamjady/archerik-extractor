package schema

import (
	"encoding/json"
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
)

// nestedFixture is a DTO graph exercising every nesting shape the contract must
// carry: a nested object (Customer, itself deep enough to truncate at Address), an
// array-of-object (List<Line>), and a map (Map<String, Label>). All stacks feed
// their own TypeDefs into this same walker, so locking the walker's emission here
// locks the nested shape for every provider; per-stack DTO -> TypeDef building is
// covered by each provider's detect_schema_test.
func nestedFixture() fakeTypes {
	return fakeTypes{
		"Order": {Name: "Order", Fields: []FieldDef{
			field("id", "String"),
			field("customer", "Customer"),         // nested object
			field("lines", "List<Line>"),          // array-of-object
			field("labels", "Map<String, Label>"), // map
		}},
		"Customer": {Name: "Customer", Fields: []FieldDef{
			field("name", "String"),
			field("address", "Address"), // pushes past depth 2 -> truncated
		}},
		"Address": {Name: "Address", Fields: []FieldDef{field("city", "String")}},
		"Line":    {Name: "Line", Fields: []FieldDef{field("sku", "String"), field("qty", "int")}},
		"Label":   {Name: "Label", Fields: []FieldDef{field("key", "String")}},
	}
}

// goldenNested is the byte-stable JSON for nestedFixture's "Order" after
// model.Sort. It documents the contract other repos ingest/render: fields
// ordered by wire name; an array carries items + hoisted element fields; a map
// carries key_type/value_type; the depth-2 boundary (address) is
// {type, truncated:true} with NO nested.
const goldenNested = `{
  "type": "Order",
  "required": "unknown",
  "nested": [
    {
      "name": "customer",
      "type": "Customer",
      "required": "unknown",
      "nested": [
        {
          "name": "address",
          "type": "Address",
          "required": "unknown",
          "truncated": true,
          "confidence": "confirmed"
        },
        {
          "name": "name",
          "type": "string",
          "required": "unknown",
          "confidence": "confirmed"
        }
      ],
      "confidence": "confirmed"
    },
    {
      "name": "id",
      "type": "string",
      "required": "unknown",
      "confidence": "confirmed"
    },
    {
      "name": "labels",
      "type": "map",
      "required": "unknown",
      "key_type": "String",
      "value_type": "Label",
      "confidence": "confirmed"
    },
    {
      "name": "lines",
      "type": "array",
      "required": "unknown",
      "items": "Line",
      "nested": [
        {
          "name": "qty",
          "type": "integer",
          "required": "required",
          "confidence": "confirmed"
        },
        {
          "name": "sku",
          "type": "string",
          "required": "unknown",
          "confidence": "confirmed"
        }
      ],
      "confidence": "confirmed"
    }
  ],
  "confidence": "confirmed"
}`

// sortedJSON walks a type, sorts it via the model's canonical sort (the ordering
// the backend diffs against), and returns indented JSON.
func sortedJSON(t *testing.T, types TypeSource, name string) string {
	t.Helper()
	svc := model.NewService("s", "s", "")
	svc.Endpoints = []model.Endpoint{{Method: "POST", Path: "/orders", Response: NewWalker(types).Type(name)}}
	model.Sort(svc)
	b, err := json.MarshalIndent(svc.Endpoints[0].Response, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestGoldenNested locks the emitted nested shape byte-for-byte.
func TestGoldenNested(t *testing.T) {
	if got := sortedJSON(t, nestedFixture(), "Order"); got != goldenNested {
		t.Errorf("nested schema JSON drifted from golden:\n--- got ---\n%s\n--- want ---\n%s", got, goldenNested)
	}
}

// TestNestedDeterministic proves the output is order-independent: the same DTO
// graph with fields declared in a different order sorts to identical bytes, so
// the backend's diffing sees no spurious change.
func TestNestedDeterministic(t *testing.T) {
	shuffled := nestedFixture()
	// Reverse the field declaration order of every type.
	for _, td := range shuffled {
		for i, j := 0, len(td.Fields)-1; i < j; i, j = i+1, j-1 {
			td.Fields[i], td.Fields[j] = td.Fields[j], td.Fields[i]
		}
	}
	if got := sortedJSON(t, shuffled, "Order"); got != goldenNested {
		t.Errorf("shuffled field order produced different bytes:\n%s", got)
	}
}

// fieldByName returns a schema's nested field by wire name, or nil.
func fieldByName(s *model.Schema, name string) *model.Schema {
	if s == nil {
		return nil
	}
	for i := range s.Nested {
		if s.Nested[i].Name == name {
			return &s.Nested[i]
		}
	}
	return nil
}

// TestConfigurableDepth proves N2: --schema-depth changes how deep the walker
// expands. Depth 1 truncates at the first nested object; depth 3 reaches a level
// the default 2 truncates; depth 0 falls back to the default (2), unchanged.
func TestConfigurableDepth(t *testing.T) {
	types := nestedFixture()

	// depth 1: the first nested object (customer) is already truncated.
	cust1 := fieldByName(NewWalkerDepth(types, 1).Type("Order"), "customer")
	if cust1 == nil || !cust1.Truncated || len(cust1.Nested) != 0 {
		t.Errorf("depth 1 customer = %+v, want truncated with no nested", cust1)
	}

	// depth 3: nesting reaches Address's fields (deeper than the default 2).
	addr3 := fieldByName(fieldByName(NewWalkerDepth(types, 3).Type("Order"), "customer"), "address")
	if addr3 == nil || addr3.Truncated {
		t.Fatalf("depth 3 address = %+v, want expanded (not truncated)", addr3)
	}
	if fieldByName(addr3, "city") == nil {
		t.Errorf("depth 3 address should expand to city, got %+v", addr3.Nested)
	}

	// depth 0 -> default (2), unchanged: address truncated, as in the golden.
	addr0 := fieldByName(fieldByName(NewWalkerDepth(types, 0).Type("Order"), "customer"), "address")
	if addr0 == nil || !addr0.Truncated {
		t.Errorf("default depth address = %+v, want truncated (depth 2)", addr0)
	}
}

// TestTruncationBoundaryShape locks N3's contract: the node at the depth limit is
// {type:<TypeName>, truncated:true} with NO nested subtree.
func TestTruncationBoundaryShape(t *testing.T) {
	order := NewWalker(nestedFixture()).Type("Order")
	var customer, address *model.Schema
	for i := range order.Nested {
		if order.Nested[i].Name == "customer" {
			customer = &order.Nested[i]
		}
	}
	if customer == nil {
		t.Fatal("customer field missing")
	}
	for i := range customer.Nested {
		if customer.Nested[i].Name == "address" {
			address = &customer.Nested[i]
		}
	}
	if address == nil {
		t.Fatal("address field missing")
	}
	if address.Type != "Address" || !address.Truncated {
		t.Errorf("boundary = %+v, want {Type:Address, Truncated:true}", address)
	}
	if len(address.Nested) != 0 {
		t.Errorf("truncated node must carry NO nested, got %d", len(address.Nested))
	}
}

// TestArrayTruncationBoundary locks the array-container form of N3's contract: an
// array whose element hits the depth limit keeps its items name, is flagged
// truncated, and carries no nested subtree.
func TestArrayTruncationBoundary(t *testing.T) {
	// At depth 1, the List<Line> element (Line) is truncated.
	lines := fieldByName(NewWalkerDepth(nestedFixture(), 1).Type("Order"), "lines")
	if lines == nil || lines.Type != "array" || lines.Items != "Line" {
		t.Fatalf("lines = %+v, want array items=Line", lines)
	}
	if !lines.Truncated {
		t.Errorf("array element past the depth limit should flag truncated: %+v", lines)
	}
	if len(lines.Nested) != 0 {
		t.Errorf("truncated array must carry NO nested, got %d", len(lines.Nested))
	}
}

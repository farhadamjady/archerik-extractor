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
// model.Sort. It documents the contract other repos ingest/render (N4/N7): fields
// ordered by wire name; an array carries items + hoisted element fields; a map
// carries key_type/value_type; the depth-2 boundary (address) is
// {type, truncated:true} with NO nested (N3).
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

// TestGoldenNested locks the emitted nested shape byte-for-byte (N1).
func TestGoldenNested(t *testing.T) {
	if got := sortedJSON(t, nestedFixture(), "Order"); got != goldenNested {
		t.Errorf("nested schema JSON drifted from golden:\n--- got ---\n%s\n--- want ---\n%s", got, goldenNested)
	}
}

// TestNestedDeterministic proves the output is order-independent: the same DTO
// graph with fields declared in a different order sorts to identical bytes, so
// the backend's diffing sees no spurious change (N1).
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

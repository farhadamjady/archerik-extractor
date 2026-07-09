package contract

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
)

func nested(s *model.Schema) map[string]model.Schema {
	m := map[string]model.Schema{}
	for _, n := range s.Nested {
		m[n.Name] = n
	}
	return m
}

func TestParseAvroRecord(t *testing.T) {
	name, s, err := ParseAvro([]byte(`{
		"type": "record",
		"name": "OrderEvent",
		"namespace": "com.acme",
		"fields": [
			{"name": "id", "type": "string"},
			{"name": "amount", "type": "double"},
			{"name": "note", "type": ["null", "string"], "default": null},
			{"name": "tags", "type": {"type": "array", "items": "string"}}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if name != "OrderEvent" || s.Type != "OrderEvent" || s.Confidence != model.Confirmed {
		t.Fatalf("record = %q/%+v", name, s)
	}
	n := nested(s)
	if n["id"].Type != "string" || n["id"].Required != model.ReqRequired {
		t.Errorf("id = %+v, want string/required", n["id"])
	}
	if n["amount"].Type != "number" {
		t.Errorf("amount = %+v, want number", n["amount"])
	}
	// nullable union with default -> nullable + optional
	if !n["note"].Nullable || n["note"].Required != model.ReqOptional {
		t.Errorf("note = %+v, want nullable/optional", n["note"])
	}
	if n["tags"].Type != "array" || n["tags"].Items != "string" {
		t.Errorf("tags = %+v, want array of string", n["tags"])
	}
}

func TestParseJSONSchema(t *testing.T) {
	name, s, err := ParseJSONSchema([]byte(`{
		"title": "Order",
		"type": "object",
		"properties": {
			"id": {"type": "string"},
			"total": {"type": "number"},
			"note": {"type": ["string", "null"]}
		},
		"required": ["id", "total"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if name != "Order" || s.Type != "Order" {
		t.Fatalf("json schema = %q/%+v", name, s)
	}
	n := nested(s)
	if n["id"].Required != model.ReqRequired || n["note"].Required != model.ReqOptional {
		t.Errorf("requiredness = id:%s note:%s", n["id"].Required, n["note"].Required)
	}
	if !n["note"].Nullable {
		t.Errorf("note should be nullable: %+v", n["note"])
	}
}

func TestParseProto(t *testing.T) {
	name, s, err := ParseProto([]byte(`
syntax = "proto3";
package com.acme;

message OrderEvent {
  string id = 1;
  double amount = 2;
  repeated string tags = 3;
  optional string note = 4;
  map<string, int32> counts = 5;
}
`))
	if err != nil {
		t.Fatal(err)
	}
	if name != "OrderEvent" || s.Type != "OrderEvent" {
		t.Fatalf("proto = %q/%+v", name, s)
	}
	n := nested(s)
	if n["id"].Type != "string" || n["amount"].Type != "number" {
		t.Errorf("scalars = %+v", s.Nested)
	}
	if n["tags"].Type != "array" || n["tags"].Items != "string" {
		t.Errorf("repeated = %+v, want array of string", n["tags"])
	}
	if n["note"].Required != model.ReqOptional {
		t.Errorf("optional note = %+v", n["note"])
	}
	if n["counts"].Type != "map" || n["counts"].ValueType != "integer" {
		t.Errorf("map = %+v, want map value integer", n["counts"])
	}
}

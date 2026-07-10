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

func TestParseOpenAPINumericCodes(t *testing.T) {
	// response codes written as YAML numbers (200:) must still resolve.
	eps, err := ParseOpenAPI([]byte(`
openapi: 3.0.1
paths:
  /x:
    get:
      responses:
        200:
          content:
            application/json:
              schema: { type: string }
`))
	if err != nil || len(eps) != 1 {
		t.Fatalf("eps=%+v err=%v", eps, err)
	}
	if eps[0].Response == nil || eps[0].Response.Type != "string" {
		t.Errorf("numeric-code response = %+v, want string", eps[0].Response)
	}
}

func TestParseOpenAPI(t *testing.T) {
	eps, err := ParseOpenAPI([]byte(`
openapi: 3.0.1
paths:
  /owners:
    get:
      responses:
        "200":
          content:
            application/json:
              schema:
                type: array
                items:
                  $ref: "#/components/schemas/Owner"
    post:
      requestBody:
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/Owner"
      responses:
        "201":
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Owner"
  /owners/{ownerId}:
    get:
      responses:
        "200":
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Owner"
components:
  schemas:
    Owner:
      type: object
      required: [firstName]
      properties:
        firstName: { type: string }
        telephone: { type: string, nullable: true }
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 3 {
		t.Fatalf("got %d endpoints, want 3: %+v", len(eps), eps)
	}
	byKey := map[string]model.Endpoint{}
	for _, e := range eps {
		byKey[e.Method+" "+e.Path] = e
	}
	if _, ok := byKey["GET /owners/{ownerId}"]; !ok {
		t.Error("GET /owners/{ownerId} missing")
	}
	post := byKey["POST /owners"]
	if post.Request == nil || post.Request.Type != "Owner" {
		t.Errorf("POST request schema = %+v, want Owner", post.Request)
	}
	list := byKey["GET /owners"]
	if list.Response == nil || list.Response.Type != "array" || list.Response.Items != "Owner" {
		t.Errorf("GET list response = %+v, want array of Owner", list.Response)
	}
	// nested field detail: required + nullable from the spec
	var first, tel *model.Schema
	for i := range post.Request.Nested {
		f := &post.Request.Nested[i]
		if f.Name == "firstName" {
			first = f
		}
		if f.Name == "telephone" {
			tel = f
		}
	}
	if first == nil || first.Required != model.ReqRequired {
		t.Errorf("firstName = %+v, want required", first)
	}
	if tel == nil || !tel.Nullable || tel.Required != model.ReqOptional {
		t.Errorf("telephone = %+v, want nullable/optional", tel)
	}
}

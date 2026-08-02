package nethttp

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/golang"
	"github.com/farhadamjady/service-discovery/internal/query"
)

func schemaFor(t *testing.T, src string) map[string]model.Endpoint {
	t.Helper()
	f, err := golang.NewParser().Parse("h.go", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	idx := &provider.Index{Types: buildTypeIndex([]*golang.File{f.(*golang.File)})}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{routeDetector{}}, idx, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := map[string]model.Endpoint{}
	for _, e := range svc.Endpoints {
		out[e.Method+" "+e.Path] = e
	}
	return out
}

// The response type comes from a `resp := CreateResp{...}` local passed to a
// JSON-encoding helper; the request from a `var in CreateReq` decoded through a
// helper whose name signals decoding. Field wire names come from json tags.
func TestHandlerBodySchema(t *testing.T) {
	src := `
package main

import (
	"encoding/json"
	"net/http"
)

type CreateReq struct {
	Name  string ` + "`json:\"name\"`" + `
	Count int    ` + "`json:\"count\"`" + `
}
type CreateResp struct {
	ID uint64 ` + "`json:\"id\"`" + `
}

func createUser(w http.ResponseWriter, r *http.Request) {
	var in CreateReq
	json.NewDecoder(r.Body).Decode(&in)
	resp := CreateResp{ID: 1}
	json.NewEncoder(w).Encode(resp)
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /users", createUser)
}`
	eps := schemaFor(t, src)
	post := eps["POST /users"]
	if post.Request == nil || post.Request.Type != "CreateReq" {
		t.Fatalf("request = %+v, want CreateReq", post.Request)
	}
	if post.Response == nil || post.Response.Type != "CreateResp" {
		t.Fatalf("response = %+v, want CreateResp", post.Response)
	}
	// json tag wire names
	names := map[string]bool{}
	for _, f := range post.Request.Nested {
		names[f.Name] = true
	}
	if !names["name"] || !names["count"] {
		t.Errorf("request fields = %v, want json-tag names name/count", names)
	}
}

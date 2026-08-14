package nethttp

import (
	"sort"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/golang"
	"github.com/farhadamjady/archerik-extractor/internal/query"
)

func schemaFor(t *testing.T, src string) map[string]model.Endpoint {
	return schemaForFiles(t, map[string]string{"h.go": src}, "h.go")
}

// schemaForFiles parses several Go files, builds the cross-file type + func
// indexes over ALL of them, then runs the route detector on `runOn` only (the
// file registering the routes). This mirrors the real pipeline, where indexers
// see every file but detection emits edges for the scanned service.
func schemaForFiles(t *testing.T, srcs map[string]string, runOn string) map[string]model.Endpoint {
	t.Helper()
	var paths []string
	for p := range srcs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	files := make([]*golang.File, 0, len(paths))
	byPath := map[string]*golang.File{}
	for _, p := range paths {
		f, err := golang.NewParser().Parse(p, []byte(srcs[p]))
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		gf := f.(*golang.File)
		files = append(files, gf)
		byPath[p] = gf
	}
	idx := &provider.Index{Types: buildTypeIndex(files), GoFuncBodies: buildFuncIndex(files)}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(byPath[runOn], []provider.Detector{routeDetector{}}, idx, nil, svc); err != nil {
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

// TestDecodeEncodeForms proves H6: request/response bodies resolve across the
// stdlib JSON forms — top-level `json.Unmarshal(b, &x)` / `json.Marshal(x)` and
// the address-of variants (`json.Marshal(&resp)`, `Encode(&resp)`), not just the
// NewDecoder/NewEncoder streaming forms.
func TestDecodeEncodeForms(t *testing.T) {
	const dtos = `
type CreateReq struct {
	Name string ` + "`json:\"name\"`" + `
}
type CreateResp struct {
	ID uint64 ` + "`json:\"id\"`" + `
}
`
	cases := map[string]string{
		// top-level Unmarshal (request) + top-level Marshal of a value (response)
		"unmarshal request, marshal value response": `
	var in CreateReq
	json.Unmarshal(body, &in)
	resp := CreateResp{ID: 1}
	json.Marshal(resp)`,
		// address-of response: Marshal(&resp)
		"marshal address-of response": `
	var in CreateReq
	json.Unmarshal(body, &in)
	resp := CreateResp{ID: 1}
	json.Marshal(&resp)`,
		// address-of response through the encoder: Encode(&resp)
		"encoder address-of response": `
	var in CreateReq
	json.NewDecoder(r.Body).Decode(&in)
	resp := CreateResp{ID: 1}
	json.NewEncoder(w).Encode(&resp)`,
	}
	for name, handlerBody := range cases {
		t.Run(name, func(t *testing.T) {
			src := `package main
import (
	"encoding/json"
	"net/http"
)
` + dtos + `
func createUser(w http.ResponseWriter, r *http.Request) {` + handlerBody + `
}
func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /users", createUser)
}`
			post := schemaFor(t, src)["POST /users"]
			if post.Request == nil || post.Request.Type != "CreateReq" {
				t.Errorf("request = %+v, want CreateReq", post.Request)
			}
			if post.Response == nil || post.Response.Type != "CreateResp" {
				t.Errorf("response = %+v, want CreateResp", post.Response)
			}
		})
	}
}

// TestHandlerBodyCrossFile proves H5: the route is registered in routes.go, but
// the handler function (and the DTOs it decodes/encodes) live in handlers.go.
// The cross-file func index lets the detector follow `createUser` into the other
// file and resolve its request/response schema.
func TestHandlerBodyCrossFile(t *testing.T) {
	routes := `
package main

import "net/http"

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /users", createUser)
}`
	handlers := `
package main

import (
	"encoding/json"
	"net/http"
)

type CreateReq struct {
	Name string ` + "`json:\"name\"`" + `
}
type CreateResp struct {
	ID uint64 ` + "`json:\"id\"`" + `
}

func createUser(w http.ResponseWriter, r *http.Request) {
	var in CreateReq
	json.NewDecoder(r.Body).Decode(&in)
	resp := CreateResp{ID: 1}
	json.NewEncoder(w).Encode(resp)
}`
	eps := schemaForFiles(t, map[string]string{
		"routes.go":   routes,
		"handlers.go": handlers,
	}, "routes.go")

	post := eps["POST /users"]
	if post.Request == nil || post.Request.Type != "CreateReq" {
		t.Fatalf("request = %+v, want CreateReq (resolved cross-file)", post.Request)
	}
	if post.Response == nil || post.Response.Type != "CreateResp" {
		t.Fatalf("response = %+v, want CreateResp (resolved cross-file)", post.Response)
	}
}

// TestErrorBranchNotResponse proves #58: a handler that encodes a
// map[string]string{"error":…} on the 404 branch (guarded by
// WriteHeader(StatusNotFound)) and the real payload on the 200 branch must NOT
// report the error envelope as the response. Here the success payload comes from
// a method-call return the walker can't type, so the honest outcome is NO
// response — never the error map.
func TestErrorBranchNotResponse(t *testing.T) {
	src := `
package main

import (
	"encoding/json"
	"net/http"
)

type User struct {
	ID string ` + "`json:\"id\"`" + `
}

type store struct{}
func (s *store) Get(id string) (User, error) { return User{}, nil }

type H struct{ s *store }

func (h *H) GetUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user, err := h.s.Get(id)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "User not found"})
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(user)
}

func main() {
	mux := http.NewServeMux()
	h := &H{}
	mux.HandleFunc("GET /users/{id}", h.GetUser)
}`
	eps := schemaFor(t, src)
	get := eps["GET /users/{id}"]
	if get.Response != nil {
		t.Fatalf("response = %+v, want nil (error envelope must not be the contract)", get.Response)
	}
}

// TestSuccessBranchWins proves the flip side: when the 200-branch payload IS a
// resolvable local, it wins over an earlier 404 error-envelope Encode.
func TestSuccessBranchWins(t *testing.T) {
	src := `
package main

import (
	"encoding/json"
	"net/http"
)

type User struct {
	ID string ` + "`json:\"id\"`" + `
}

func getUser(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "bad"})
		return
	}
	user := User{ID: "1"}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(user)
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", getUser)
}`
	eps := schemaFor(t, src)
	get := eps["GET /users/{id}"]
	if get.Response == nil || get.Response.Type != "User" {
		t.Fatalf("response = %+v, want User (success branch wins over error envelope)", get.Response)
	}
}

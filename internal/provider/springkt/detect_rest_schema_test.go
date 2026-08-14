package springkt

import (
	"fmt"
	"testing"

	"github.com/farhadamjady/archerik-extractor/internal/model"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/kotlin"
	"github.com/farhadamjady/archerik-extractor/internal/query"
)

// endpointsWithSchema runs the REST detector over a controller source with a DTO
// type index built across the controller plus any DTO sources. DTOs live in
// separate files, mirroring the real multi-file pipeline (and sidestepping a
// tree-sitter-kotlin quirk that mis-parses annotations on a class when another
// top-level declaration follows it in the SAME file).
func endpointsWithSchema(t *testing.T, ctrlSrc string, dtoSrcs ...string) []model.Endpoint {
	t.Helper()
	ctrl, err := kotlin.NewParser().Parse("Controller.kt", []byte(ctrlSrc))
	if err != nil {
		t.Fatalf("parse controller: %v", err)
	}
	files := []*kotlin.File{ctrl.(*kotlin.File)}
	for i, d := range dtoSrcs {
		df, err := kotlin.NewParser().Parse(fmt.Sprintf("Dto%d.kt", i), []byte(d))
		if err != nil {
			t.Fatalf("parse dto %d: %v", i, err)
		}
		files = append(files, df.(*kotlin.File))
	}
	idx := &provider.Index{Types: kotlin.IndexTypes(files, nil)}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(ctrl, []provider.Detector{restDetector{}}, idx, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)
	return svc.Endpoints
}

func findEndpoint(eps []model.Endpoint, method, path string) *model.Endpoint {
	for i := range eps {
		if eps[i].Method == method && eps[i].Path == path {
			return &eps[i]
		}
	}
	return nil
}

func TestRESTRequestResponseSchema(t *testing.T) {
	ctrl := `package x
@RestController
@RequestMapping("/orders")
class OrderController(val repo: OrderRepository) {
    @PostMapping
    fun create(@RequestBody req: CreateOrder): Order = repo.save(req)

    @GetMapping("/{id}")
    fun get(@PathVariable id: Long): Order? = repo.find(id)

    @DeleteMapping("/{id}")
    fun delete(@PathVariable id: Long): Unit = repo.delete(id)
}`
	dtos := `package x
data class CreateOrder(val item: String, val qty: Int)
data class Order(val id: Long, val item: String, val customer: Customer)
data class Customer(val name: String, val vip: Boolean)`

	eps := endpointsWithSchema(t, ctrl, dtos)

	// POST /orders: request = CreateOrder, response = Order (with nested Customer).
	post := findEndpoint(eps, "POST", "/orders")
	if post == nil {
		t.Fatalf("POST /orders not found in %v", eps)
	}
	if post.Request == nil || post.Request.Type != "CreateOrder" {
		t.Fatalf("POST request = %+v, want CreateOrder", post.Request)
	}
	rf := nestedTypes(post.Request)
	if rf["item"] != "string" || rf["qty"] != "integer" {
		t.Errorf("CreateOrder fields = %v, want item:string qty:integer", rf)
	}
	if post.Response == nil || post.Response.Type != "Order" {
		t.Fatalf("POST response = %+v, want Order", post.Response)
	}
	var customer *model.Schema
	for i := range post.Response.Nested {
		if post.Response.Nested[i].Name == "customer" {
			customer = &post.Response.Nested[i]
		}
	}
	if customer == nil || customer.Type != "Customer" || len(customer.Nested) != 2 {
		t.Fatalf("Order.customer not expanded: %+v", customer)
	}

	// GET /orders/{id}: nullable return (`Order?`) marks the response nullable,
	// and there is no request body.
	get := findEndpoint(eps, "GET", "/orders/{id}")
	if get == nil || get.Response == nil {
		t.Fatal("GET /orders/{id} response missing")
	}
	if !get.Response.Nullable {
		t.Errorf("GET response should be nullable (Order?)")
	}
	if get.Request != nil {
		t.Errorf("GET should have no request body, got %+v", get.Request)
	}

	// DELETE returns Unit -> no response body.
	del := findEndpoint(eps, "DELETE", "/orders/{id}")
	if del == nil {
		t.Fatal("DELETE /orders/{id} not found")
	}
	if del.Response != nil {
		t.Errorf("DELETE (Unit) should have no response body, got %+v", del.Response)
	}
}

func nestedTypes(s *model.Schema) map[string]string {
	m := map[string]string{}
	for _, f := range s.Nested {
		m[f.Name] = f.Type
	}
	return m
}

// TestExpressionBodyResponseInference proves #66: an expression-body handler with
// no declared return type is typed from the delegate call — a query method
// declared in the repository (findByLastName) and an inherited Spring Data method
// (findAll, synthesised from CrudRepository<Customer, Long>).
func TestExpressionBodyResponseInference(t *testing.T) {
	ctrl := `package x
@RestController
class CustomerController(private val repository: CustomerRepository) {
	@GetMapping("/customers")
	fun findAll() = repository.findAll()
	@GetMapping("/customers/{lastName}")
	fun findByLastName(@PathVariable lastName: String) = repository.findByLastName(lastName)
}`
	repo := `package x
interface CustomerRepository : CrudRepository<Customer, Long> {
	fun findByLastName(lastName: String): Iterable<Customer>
}`
	entity := `package x
data class Customer(val id: Long, val firstName: String, val lastName: String)`

	cf, _ := kotlin.NewParser().Parse("Controller.kt", []byte(ctrl))
	rf, _ := kotlin.NewParser().Parse("Repo.kt", []byte(repo))
	ef, _ := kotlin.NewParser().Parse("Customer.kt", []byte(entity))
	files := []*kotlin.File{cf.(*kotlin.File), rf.(*kotlin.File), ef.(*kotlin.File)}
	idx := &provider.Index{Types: kotlin.IndexTypes(files, nil)}
	parsed := map[string]provider.ParsedFile{"Controller.kt": files[0], "Repo.kt": files[1], "Customer.kt": files[2]}
	if err := (methodIndexer{}).Index(&provider.IndexContext{Parsed: parsed}, idx); err != nil {
		t.Fatalf("methodIndexer: %v", err)
	}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(cf, []provider.Detector{restDetector{}}, idx, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	model.Sort(svc)

	all := findEndpoint(svc.Endpoints, "GET", "/customers")
	if all == nil || all.Response == nil || all.Response.Type != "array" || all.Response.Items != "Customer" {
		t.Fatalf("GET /customers = %+v, want array of Customer (Spring Data findAll)", all)
	}
	byName := findEndpoint(svc.Endpoints, "GET", "/customers/{lastName}")
	if byName == nil || byName.Response == nil || byName.Response.Items != "Customer" {
		t.Fatalf("GET /customers/{lastName} = %+v, want array of Customer (declared findByLastName)", byName)
	}
}

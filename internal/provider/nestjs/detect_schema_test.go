package nestjs

import (
	"testing"

	"github.com/farhadamjady/service-discovery/internal/model"
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/lang/tsjs"
	"github.com/farhadamjady/service-discovery/internal/query"
)

// schemaFor parses one source, indexes its types, runs the REST detector, and
// returns the endpoints keyed by "VERB path".
func schemaFor(t *testing.T, src string) map[string]model.Endpoint {
	t.Helper()
	f, err := tsjs.NewParser().Parse("c.ts", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	idx := &provider.Index{Types: buildTypeIndex([]*tsjs.File{f.(*tsjs.File)})}
	svc := model.NewService("s", "s", "")
	if err := query.New().Run(f, []provider.Detector{restDetector{}}, idx, nil, svc); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := map[string]model.Endpoint{}
	for _, e := range svc.Endpoints {
		out[e.Method+" "+e.Path] = e
	}
	return out
}

func TestResponseAndRequestSchema(t *testing.T) {
	src := `
		export class UserDto {
			readonly name: string;
			readonly age: number;
			readonly tags: string[];
		}
		export interface UserRO { user: UserDto; token: string; }
		@Controller('users')
		export class UsersController {
			@Get(':id') findOne(): Promise<UserRO> { return null; }
			@Post() create(@Body() dto: UserDto) {}
		}`
	eps := schemaFor(t, src)

	get := eps["GET /users/{id}"]
	if get.Response == nil || get.Response.Type != "UserRO" {
		t.Fatalf("GET response = %+v, want UserRO", get.Response)
	}
	// UserRO.user nests UserDto -> {name, age, tags[]}
	var user *model.Schema
	for i := range get.Response.Nested {
		if get.Response.Nested[i].Name == "user" {
			user = &get.Response.Nested[i]
		}
	}
	if user == nil || user.Type != "UserDto" || len(user.Nested) != 3 {
		t.Fatalf("UserRO.user = %+v, want nested UserDto{name,age,tags}", user)
	}

	post := eps["POST /users"]
	if post.Request == nil || post.Request.Type != "UserDto" {
		t.Fatalf("POST request = %+v, want UserDto", post.Request)
	}
	for _, f := range post.Request.Nested {
		if f.Name == "tags" && (f.Type != "array" || f.Items != "string") {
			t.Errorf("tags = %+v, want array of string", f)
		}
	}
}

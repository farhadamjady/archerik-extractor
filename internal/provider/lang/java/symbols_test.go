package java

import "testing"

func symbols(t *testing.T, srcs ...string) *Symbols {
	t.Helper()
	var files []*File
	for i, s := range srcs {
		pf, err := NewParser().Parse(string(rune('A'+i))+".java", []byte(s))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		files = append(files, pf.(*File))
	}
	return IndexSymbols(files)
}

func TestConstantStringField(t *testing.T) {
	s := symbols(t, `class OrderTopics {
		public static final String ORDERS = "orders";
		static final String SHIPMENTS = "shipments";
	}`)

	for _, tc := range []struct{ ref, want string }{
		{"OrderTopics.ORDERS", "orders"},
		{"OrderTopics.SHIPMENTS", "shipments"},
		{"ORDERS", "orders"}, // bare, unambiguous
	} {
		if v, ok := s.Constant(tc.ref); !ok || v != tc.want {
			t.Errorf("Constant(%q) = (%q, %v), want %q", tc.ref, v, ok, tc.want)
		}
	}
}

func TestNonStaticIgnored(t *testing.T) {
	s := symbols(t, `class C { String notAConst = "x"; }`)
	if _, ok := s.Constant("C.notAConst"); ok {
		t.Error("non-static field should not be indexed")
	}
}

func TestEnumConstantWithStringArg(t *testing.T) {
	s := symbols(t, `enum Topic {
		ORDERS("orders"),
		SHIPMENTS("shipments");
		private final String v;
		Topic(String v) { this.v = v; }
	}`)
	if v, ok := s.Constant("Topic.ORDERS"); !ok || v != "orders" {
		t.Errorf("Constant(Topic.ORDERS) = (%q, %v), want orders", v, ok)
	}
}

func TestBareConflictDropped(t *testing.T) {
	// Same bare name, different values, in two classes -> bare lookup ambiguous.
	s := symbols(t,
		`class A { public static final String NAME = "a-name"; }`,
		`class B { public static final String NAME = "b-name"; }`)

	// Qualified still works...
	if v, _ := s.Constant("A.NAME"); v != "a-name" {
		t.Errorf("A.NAME = %q", v)
	}
	if v, _ := s.Constant("B.NAME"); v != "b-name" {
		t.Errorf("B.NAME = %q", v)
	}
	// ...but the ambiguous bare name is dropped.
	if _, ok := s.Constant("NAME"); ok {
		t.Error("ambiguous bare NAME should not resolve")
	}
}

func TestUnknownReference(t *testing.T) {
	s := symbols(t, `class C { public static final String X = "x"; }`)
	if _, ok := s.Constant("C.MISSING"); ok {
		t.Error("unknown reference should not resolve")
	}
}

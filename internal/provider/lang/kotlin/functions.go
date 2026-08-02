package kotlin

// Function navigation helpers over the tree-sitter-kotlin grammar, used by
// framework providers to read a handler's request/response types.
//
// A function_declaration lays out as: modifiers, simple_identifier (name),
// function_value_parameters, <return type>?, function_body. The return type is a
// user_type/nullable_type child positioned AFTER the parameter list (a receiver
// type on an extension function sits before it, so position matters). Inside
// function_value_parameters the grammar emits each parameter's annotations as a
// `parameter_modifiers` SIBLING immediately before its `parameter` node — not as
// a child — so Params re-associates the two.

// Param pairs a function parameter with its (sibling) modifiers node, which
// carries the parameter's annotations (@RequestBody, @PathVariable, ...). Mods
// is an invalid Node when the parameter has no modifiers.
type Param struct {
	Node Node // the `parameter` node (simple_identifier + type)
	Mods Node // the preceding `parameter_modifiers`, or invalid
}

// Params returns a function_declaration's value parameters, each paired with the
// modifiers that precede it.
func Params(fn Node) []Param {
	fvp := ChildByType(fn, "function_value_parameters")
	if !fvp.Valid() {
		return nil
	}
	var out []Param
	var mods Node
	for _, c := range NamedChildren(fvp) {
		switch c.Type() {
		case "parameter_modifiers":
			mods = c
		case "parameter":
			out = append(out, Param{Node: c, Mods: mods})
			mods = Node{} // consumed; the next parameter has its own (or none)
		}
	}
	return out
}

// ReturnType returns a function's declared return type text (nullability marker
// stripped) and whether it was nullable (`T?`). It reads the type child that
// follows function_value_parameters, so an extension-function receiver type
// (which precedes the parameters) is not mistaken for the return type. An absent
// (inferred) return type yields ("", false).
func ReturnType(fn Node) (typ string, nullable bool) {
	seenParams := false
	for _, c := range NamedChildren(fn) {
		if c.Type() == "function_value_parameters" {
			seenParams = true
			continue
		}
		if !seenParams {
			continue
		}
		switch c.Type() {
		case "nullable_type":
			if inner := ChildByType(c, "user_type"); inner.Valid() {
				return inner.Text(), true
			}
		case "user_type":
			return c.Text(), false
		}
	}
	return "", false
}

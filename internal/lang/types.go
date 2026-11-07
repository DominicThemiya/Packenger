package lang

import (
	"fmt"
	"strings"
	"unicode"
)

// Types are written and stored as canonical strings:
//
//	int  u8  bool  str     primitive types, declared by packages
//	Node                   a struct, declared with the struct syntax
//	ptr T                  pointer to T
//	[T]                    slice of T
//	fn(A, B) -> R          function taking A and B, returning R
//	fn(A)                  function without a result
//	?                      unknown: matches any type (null, and the first parse pass)
//	""                     no value (a function without a result)
//
// In package constraints a type may also contain type variables (a single
// capital letter, e.g. ptr T) and slot references ({slot}: the type of that
// slot), and fn(...) -> R matches any function type, binding R to its result
// ("" if it has none).

type typ struct {
	kind   string // name, ptr, slice, fn, var, slot, wild
	name   string
	elem   *typ
	params []*typ // fn
	ret    *typ   // fn: nil if the function has no result
	any    bool   // fn(...): any parameters (constraints only)
}

// FnType renders the type of a function with these parameter and result
// types (ret "" for none).
func FnType(params []string, ret string) string {
	s := "fn(" + strings.Join(params, ", ") + ")"
	if ret != "" {
		s += " -> " + ret
	}
	return s
}

// IsFn reports whether t is a function type.
func IsFn(t string) bool { return strings.HasPrefix(t, "fn(") }

// FnParts splits a function type into its parameter and result types.
func FnParts(t string) (params []string, ret string, ok bool) {
	ft, err := parseType(t)
	if err != nil || ft.kind != "fn" || ft.any {
		return nil, "", false
	}
	for _, p := range ft.params {
		params = append(params, p.String())
	}
	if ft.ret != nil {
		ret = ft.ret.String()
	}
	return params, ret, true
}

func parseType(s string) (*typ, error) {
	t, rest, err := parseTypePrefix(strings.TrimSpace(s))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(rest) != "" {
		return nil, fmt.Errorf("unexpected %q in type %q", rest, s)
	}
	return t, nil
}

func parseTypePrefix(s string) (*typ, string, error) {
	s = strings.TrimLeft(s, " ")
	switch {
	case s == "":
		return nil, "", fmt.Errorf("missing type")
	case strings.HasPrefix(s, "?"):
		return &typ{kind: "wild"}, s[1:], nil
	case strings.HasPrefix(s, "["):
		elem, rest, err := parseTypePrefix(s[1:])
		if err != nil {
			return nil, "", err
		}
		rest = strings.TrimLeft(rest, " ")
		if !strings.HasPrefix(rest, "]") {
			return nil, "", fmt.Errorf("missing ']' in type")
		}
		return &typ{kind: "slice", elem: elem}, rest[1:], nil
	case strings.HasPrefix(s, "{"):
		end := strings.IndexByte(s, '}')
		if end < 0 {
			return nil, "", fmt.Errorf("missing '}' in type")
		}
		return &typ{kind: "slot", name: strings.TrimSpace(s[1:end])}, s[end+1:], nil
	case strings.HasPrefix(s, "fn("):
		return parseFnType(s[3:])
	}
	end := 0
	for end < len(s) && (unicode.IsLetter(rune(s[end])) || unicode.IsDigit(rune(s[end])) || s[end] == '_') {
		end++
	}
	if end == 0 {
		return nil, "", fmt.Errorf("unexpected %q in type", s)
	}
	word, rest := s[:end], s[end:]
	switch {
	case word == "ptr":
		elem, rest, err := parseTypePrefix(rest)
		if err != nil {
			return nil, "", err
		}
		return &typ{kind: "ptr", elem: elem}, rest, nil
	case word == "any":
		return &typ{kind: "wild"}, rest, nil
	case len(word) == 1 && unicode.IsUpper(rune(word[0])):
		return &typ{kind: "var", name: word}, rest, nil
	}
	return &typ{kind: "name", name: word}, rest, nil
}

// parseFnType reads the rest of a function type after "fn(".
func parseFnType(s string) (*typ, string, error) {
	t := &typ{kind: "fn"}
	rest := strings.TrimLeft(s, " ")
	switch {
	case strings.HasPrefix(rest, "...)"):
		t.any, rest = true, rest[4:]
	case strings.HasPrefix(rest, ")"):
		rest = rest[1:]
	default:
		for {
			p, r, err := parseTypePrefix(rest)
			if err != nil {
				return nil, "", err
			}
			t.params = append(t.params, p)
			r = strings.TrimLeft(r, " ")
			if strings.HasPrefix(r, ",") {
				rest = r[1:]
				continue
			}
			if !strings.HasPrefix(r, ")") {
				return nil, "", fmt.Errorf("missing ')' in function type")
			}
			rest = r[1:]
			break
		}
	}
	if r := strings.TrimLeft(rest, " "); strings.HasPrefix(r, "->") {
		ret, r, err := parseTypePrefix(r[2:])
		if err != nil {
			return nil, "", err
		}
		t.ret, rest = ret, r
	}
	return t, rest, nil
}

func (t *typ) String() string {
	switch t.kind {
	case "fn":
		var ps []string
		for _, p := range t.params {
			ps = append(ps, p.String())
		}
		if t.any {
			ps = []string{"..."}
		}
		ret := ""
		if t.ret != nil {
			ret = t.ret.String()
		}
		return FnType(ps, ret)
	case "ptr":
		return "ptr " + t.elem.String()
	case "slice":
		return "[" + t.elem.String() + "]"
	case "wild":
		return "?"
	case "slot":
		return "{" + t.name + "}"
	}
	return t.name
}

// isNull reports whether t is null's type, ptr ?.
func isNull(t *typ) bool { return t.kind == "ptr" && t.elem.kind == "wild" }

// unify reports whether two concrete types are compatible; ? matches anything.
func unify(a, b *typ) bool {
	if a.kind == "wild" || b.kind == "wild" {
		return true
	}
	if isNull(a) && b.kind == "fn" || isNull(b) && a.kind == "fn" {
		return true // null is also a function value
	}
	if a.kind != b.kind || a.name != b.name {
		return false
	}
	if a.kind == "fn" {
		if len(a.params) != len(b.params) || (a.ret == nil) != (b.ret == nil) {
			return false
		}
		for i := range a.params {
			if !unify(a.params[i], b.params[i]) {
				return false
			}
		}
		return a.ret == nil || unify(a.ret, b.ret)
	}
	if a.elem != nil {
		return unify(a.elem, b.elem)
	}
	return true
}

// Compatible reports whether a value of type got can be used where want is
// expected (both concrete).
func Compatible(want, got string) bool {
	if want == got {
		return true
	}
	a, err1 := parseType(want)
	b, err2 := parseType(got)
	return err1 == nil && err2 == nil && unify(a, b)
}

// binder matches constraint patterns against actual types, collecting
// bindings for type variables. slotType gives the type of a slot that has
// already been parsed.
type binder struct {
	vars     map[string]*typ
	slotType func(name string) (string, bool)
}

func newBinder(slotType func(string) (string, bool)) *binder {
	return &binder{vars: map[string]*typ{}, slotType: slotType}
}

func (b *binder) match(pat *typ, actual *typ) bool {
	if actual.kind == "wild" {
		if pat.kind == "var" {
			if _, ok := b.vars[pat.name]; !ok {
				b.vars[pat.name] = actual
			}
		}
		return true
	}
	switch pat.kind {
	case "wild":
		return true
	case "var":
		if bound, ok := b.vars[pat.name]; ok {
			return unify(bound, actual)
		}
		b.vars[pat.name] = actual
		return true
	case "slot":
		s, ok := b.slotType(pat.name)
		if !ok {
			return true // slot not given (optional): no constraint
		}
		t, err := parseType(s)
		return err == nil && unify(t, actual)
	case "name":
		return actual.kind == "name" && actual.name == pat.name
	case "ptr", "slice":
		return actual.kind == pat.kind && b.match(pat.elem, actual.elem)
	case "fn":
		if actual.kind != "fn" {
			return false
		}
		if !pat.any {
			if len(pat.params) != len(actual.params) {
				return false
			}
			for i := range pat.params {
				if !b.match(pat.params[i], actual.params[i]) {
					return false
				}
			}
		}
		switch {
		case pat.ret == nil:
			return pat.any || actual.ret == nil
		case actual.ret == nil:
			// fn(...) -> R on a function without a result: R is no value
			if pat.ret.kind == "var" {
				if bound, ok := b.vars[pat.ret.name]; ok {
					return bound.kind == "name" && bound.name == ""
				}
				b.vars[pat.ret.name] = &typ{kind: "name", name: ""}
				return true
			}
			return false
		}
		return b.match(pat.ret, actual.ret)
	}
	return false
}

// Matches checks a constraint string against an actual type string.
func (b *binder) Matches(constraint, actual string) (bool, error) {
	if constraint == "" || constraint == "any" {
		return true, nil
	}
	pat, err := parseType(constraint)
	if err != nil {
		return false, err
	}
	if actual == "" {
		return false, nil
	}
	got, err := parseType(actual)
	if err != nil {
		return false, err
	}
	return b.match(pat, got), nil
}

// Subst fills type variables and slot references into a result type.
func (b *binder) Subst(result string) (string, error) {
	t, err := parseType(result)
	if err != nil {
		return "", err
	}
	var walk func(*typ) (*typ, error)
	walk = func(t *typ) (*typ, error) {
		switch t.kind {
		case "var":
			if v, ok := b.vars[t.name]; ok {
				return v, nil
			}
			return &typ{kind: "wild"}, nil
		case "slot":
			s, ok := b.slotType(t.name)
			if !ok {
				return nil, fmt.Errorf("result type names slot {%s}, which is missing", t.name)
			}
			if s == "" {
				return &typ{kind: "name", name: ""}, nil
			}
			return parseType(s)
		case "ptr", "slice":
			e, err := walk(t.elem)
			if err != nil {
				return nil, err
			}
			return &typ{kind: t.kind, elem: e}, nil
		case "fn":
			out := &typ{kind: "fn", any: t.any}
			for _, p := range t.params {
				w, err := walk(p)
				if err != nil {
					return nil, err
				}
				out.params = append(out.params, w)
			}
			if t.ret != nil {
				w, err := walk(t.ret)
				if err != nil {
					return nil, err
				}
				out.ret = w
			}
			return out, nil
		}
		return t, nil
	}
	out, err := walk(t)
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

// ---------------------------------------------------------------- layout

// Struct is a declared struct type.
type Struct struct {
	Name   string
	Fields []*Field
	Size   int
}

type Field struct {
	Name   string
	Type   string
	Offset int
}

func (s *Struct) Field(name string) *Field {
	for _, f := range s.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// Types knows the size of every type: primitives declared by packages, and
// structs declared in the program.
type Types struct {
	Prims   map[string]int
	Structs map[string]*Struct
}

// Size is the number of bytes a value of type t occupies in memory (in a
// field or slice element). Every value occupies one register.
func (ts *Types) Size(t string) (int, error) {
	switch {
	case strings.HasPrefix(t, "ptr ") || strings.HasPrefix(t, "[") || IsFn(t):
		return 8, nil
	case t == "?":
		return 8, nil
	}
	if n, ok := ts.Prims[t]; ok {
		return n, nil
	}
	if _, ok := ts.Structs[t]; ok {
		return 0, fmt.Errorf("struct %s can only be used through a pointer (ptr %s)", t, t)
	}
	return 0, fmt.Errorf("unknown type %s", t)
}

// Elem is the element type of a slice, the pointee of a pointer, and u8 for
// str.
func Elem(t string) string {
	switch {
	case strings.HasPrefix(t, "ptr "):
		return t[4:]
	case strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]"):
		return t[1 : len(t)-1]
	case t == "str":
		return "u8"
	}
	return ""
}

// layout assigns field offsets: each field aligned to its size, the whole
// struct rounded up to 8 bytes.
func (ts *Types) layout(s *Struct) error {
	off := 0
	for _, f := range s.Fields {
		size, err := ts.Size(f.Type)
		if err != nil {
			return fmt.Errorf("field %s of %s: %v", f.Name, s.Name, err)
		}
		off = (off + size - 1) / size * size
		f.Offset = off
		off += size
	}
	s.Size = (off + 7) &^ 7
	if s.Size == 0 {
		s.Size = 8
	}
	return nil
}

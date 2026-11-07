package lang

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/DominicThemiya/Packenger/internal/pkgfile"
)

// ---------------------------------------------------------------- language tables

// Rule is one pattern of one syntax package.
type Rule struct {
	Pkg  *pkgfile.File
	Kind string
	Syn  *pkgfile.Syntax
	Pat  *pkgfile.Pattern
	tmpl []Token // lexed expand template
}

// Lang is the grammar assembled from the loaded syntax packages. Nothing in
// it is built in.
type Lang struct {
	Pkgs     map[string]*pkgfile.File // kind -> package
	stmtLit  map[string][]*Rule
	stmtSlot []*Rule
	exprLit  map[string][]*Rule
	exprSlot []*Rule
	infix    map[string][]*Rule
	prefix   map[string][]*Rule
	postfix  map[string][]*Rule
	Prims    map[string]int // primitive types declared by packages, with their sizes
	closers  map[string]bool
	keywords map[string]bool
	Puncts   []string
}

var coreKinds = map[string]bool{"block": true, "int": true, "str": true, "var": true, "ident": true, "func": true, "newfunc": true, "args": true, "params": true, "program": true,
	"type": true, "field": true, "fields": true, "fielddecl": true, "newtype": true, "newconst": true, "group": true, "groups": true}

func isWord(s string) bool {
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return false
		}
	}
	return s != ""
}

func NewLang(pkgs []*pkgfile.File) (*Lang, error) {
	L := &Lang{
		Pkgs:    map[string]*pkgfile.File{},
		stmtLit: map[string][]*Rule{}, exprLit: map[string][]*Rule{},
		infix: map[string][]*Rule{}, prefix: map[string][]*Rule{}, postfix: map[string][]*Rule{},
		Prims: map[string]int{}, closers: map[string]bool{}, keywords: map[string]bool{},
	}
	typeOwner := map[string]string{}
	punct := map[string]bool{}
	for _, f := range pkgs {
		kind := f.Kind()
		if len(f.Syntax) > 0 {
			// a syntax package's name is the kind of node it makes
			if coreKinds[kind] {
				return nil, fmt.Errorf("%s: package name %q is reserved by the compiler", f.Origin, kind)
			}
			if other, ok := L.Pkgs[kind]; ok {
				return nil, fmt.Errorf("two loaded packages both define %q: %s and %s", kind, other.Name, f.Name)
			}
			L.Pkgs[kind] = f
		}
		for _, t := range f.Types {
			if o, ok := typeOwner[t.Name]; ok {
				return nil, fmt.Errorf("type %s is declared by both %s and %s", t.Name, o, f.Name)
			}
			typeOwner[t.Name] = f.Name
			L.Prims[t.Name] = t.Size
		}
		for _, s := range f.Syntax {
			for _, p := range s.Patterns {
				r := &Rule{Pkg: f, Kind: kind, Syn: s, Pat: p}
				for _, lit := range p.Literals() {
					if isWord(lit) {
						L.keywords[lit] = true
					} else {
						punct[lit] = true
					}
				}
				for _, c := range p.Closers() {
					L.closers[c] = true
				}
				first := p.Elems[0]
				switch s.Kind {
				case "infix", "prefix", "postfix":
					table, op := L.infix, p.Elems[1].Lit
					switch s.Kind {
					case "prefix":
						table, op = L.prefix, first.Lit
					case "postfix":
						table = L.postfix
					}
					// several patterns may share an operator (chosen by operand
					// types), but only within one package
					if o := table[op]; len(o) > 0 && (o[0].Pkg != f || o[0].Syn.Prec != s.Prec) {
						return nil, fmt.Errorf("%s operator %q is defined by both %s and %s", s.Kind, op, o[0].Pkg.Name, f.Name)
					}
					table[op] = append(table[op], r)
				case "expr":
					if first.Lit != "" {
						L.exprLit[first.Lit] = append(L.exprLit[first.Lit], r)
					} else {
						L.exprSlot = append(L.exprSlot, r)
					}
				default: // statement, function, struct
					if first.Lit != "" {
						L.stmtLit[first.Lit] = append(L.stmtLit[first.Lit], r)
					} else {
						L.stmtSlot = append(L.stmtSlot, r)
					}
				}
			}
		}
	}
	// Longer slot-first patterns are tried first, so "x = 1" (assignment)
	// wins over a bare expression statement.
	sort.SliceStable(L.stmtSlot, func(i, j int) bool { return len(L.stmtSlot[i].Pat.Elems) > len(L.stmtSlot[j].Pat.Elems) })
	for p := range punct {
		L.Puncts = append(L.Puncts, p)
	}
	sort.Strings(L.Puncts)
	for _, rs := range [][]*Rule{L.stmtSlot, L.exprSlot} {
		for _, r := range rs {
			if err := L.lexTemplate(r); err != nil {
				return nil, err
			}
		}
	}
	for _, m := range []map[string][]*Rule{L.stmtLit, L.exprLit} {
		for _, rs := range m {
			for _, r := range rs {
				if err := L.lexTemplate(r); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, m := range []map[string][]*Rule{L.infix, L.prefix, L.postfix} {
		for _, rs := range m {
			for _, r := range rs {
				if err := L.lexTemplate(r); err != nil {
					return nil, err
				}
			}
		}
	}
	return L, nil
}

// forms counts the statement and expression patterns.
func (L *Lang) forms() int {
	n := len(L.stmtSlot) + len(L.exprSlot)
	for _, rs := range L.stmtLit {
		n += len(rs)
	}
	for _, rs := range L.exprLit {
		n += len(rs)
	}
	return n
}

func (L *Lang) lexTemplate(r *Rule) error {
	if r.Pkg.Expand == nil || r.tmpl != nil {
		return nil
	}
	toks, err := Lex(strings.Join(r.Pkg.Expand, "\n"), r.Pkg.Name+" (expand)", L.Puncts, true)
	if err != nil {
		return err
	}
	r.tmpl = toks[:len(toks)-1] // drop EOF
	return nil
}

// ---------------------------------------------------------------- AST

type Var struct {
	Name   string
	Type   string
	Param  bool
	Global bool
	Index  int
}

type Frame struct {
	Name   string
	scopes []map[string]*Var
	Slots  int
	Params int
	Ret    string // result type; "" if the function returns nothing
	Top    bool   // the top level: a bare return exits with 0
}

func newFrame(name string) *Frame {
	return &Frame{Name: name, scopes: []map[string]*Var{{}}}
}

// Size is the stack space for the frame's locals, kept 16-byte aligned.
func (f *Frame) Size() int { return (f.Slots*8 + 15) &^ 15 }

type Node struct {
	ID       int
	Kind     string
	Rule     *Rule
	Pos      Pos
	Type     string
	Slots    map[string]*Node
	Order    []string // slot names in pattern order: the canonical walk order
	Items    []*Node
	Int      int64
	Str      string
	Ident    string
	Var      *Var
	Field    *Field
	Frame    *Frame
	Loop     *Node    // the enclosing loop, for break and continue
	Expanded []string // packages that were expanded into this node

	toks       []Token
	start, end int
}

type Program struct {
	Main    *Node // block of top-level statements
	Frame   *Frame
	Funcs   []*Node
	Structs []*Node
	Top     []*Node // every top-level item, in source order (library code first)
	Types   *Types
	Globals []*Var // in declaration order
}

// Unit is one stream of top-level source: library code from a package, or
// the program file.
type Unit struct {
	Name string
	Toks []Token
	Lib  bool // library code: only functions, structs and top-level declarations
}

// Sig is a function's signature.
type Sig struct {
	Params []string
	Ret    string
}

// setSlot fills a slot, remembering the order slots were filled in.
func (n *Node) setSlot(name string, c *Node) {
	if _, ok := n.Slots[name]; !ok {
		n.Order = append(n.Order, name)
	}
	n.Slots[name] = c
}

// Children returns the node's slots in order, then its items.
func (n *Node) Children() []*Node {
	out := make([]*Node, 0, len(n.Order)+len(n.Items))
	for _, name := range n.Order {
		out = append(out, n.Slots[name])
	}
	return append(out, n.Items...)
}

// number gives every node its ID: a pre-order walk of the top-level items
// in source order, starting at 1. IDs name labels in the generated code, so
// they depend only on the final tree, never on parse attempts that failed.
func number(prog *Program) {
	id := 0
	var walk func(*Node)
	walk = func(n *Node) {
		id++
		n.ID = id
		for _, c := range n.Children() {
			walk(c)
		}
	}
	for _, n := range prog.Top {
		walk(n)
	}
}

// ---------------------------------------------------------------- parser

type Parser struct {
	L        *Lang
	toks     []Token
	i        int
	frame    *Frame
	funcs    map[string]*Node
	undo     []func()
	far      error
	farPos   Pos
	farHard  bool // far is a definite error (see failHard)
	depth    int
	lenient  bool            // first pass: names defined later in the file are allowed
	sigs     map[string]*Sig // second pass: every function's signature, from the first pass
	types    *Types
	declared map[string]bool  // structs, globals and constants declared so far in this pass
	globals  map[string]*Var  // second pass: every global, from the first pass
	consts   map[string]*Node // constant values
	gorder   []*Var
	loops    []*Node // enclosing loops, innermost last
}

func newParser(L *Lang) *Parser {
	return &Parser{L: L, funcs: map[string]*Node{}, sigs: map[string]*Sig{},
		types:    &Types{Prims: L.Prims, Structs: map[string]*Struct{}},
		declared: map[string]bool{}, globals: map[string]*Var{}, consts: map[string]*Node{}}
}

// Parse parses a program in two passes. The first pass is lenient about
// names defined later in the file and collects every function signature and
// struct; the second pass parses again with those known and checks all
// types. So functions and structs can be used before they are defined.
func Parse(L *Lang, units []Unit) (*Program, error) {
	p1 := newParser(L)
	p1.lenient = true
	first, err := p1.parseProgram(units)
	if err != nil {
		return nil, err
	}
	p2 := newParser(L)
	p2.types.Structs = p1.types.Structs
	for i, v := range first.Globals {
		// globals are numbered in declaration order, so a backend can give
		// each one a fixed address
		p2.globals[v.Name] = &Var{Name: v.Name, Type: v.Type, Global: true, Index: i}
	}
	for name, c := range p1.consts {
		p2.consts[name] = c
	}
	for _, f := range first.Funcs {
		sig := &Sig{Ret: f.Frame.Ret}
		for _, name := range f.Order {
			if c := f.Slots[name]; c.Kind == "params" {
				for _, prm := range c.Items {
					sig.Params = append(sig.Params, prm.Type)
				}
			}
		}
		p2.sigs[f.Frame.Name] = sig
	}
	prog, err := p2.parseProgram(units)
	if err != nil {
		return nil, err
	}
	return prog, p2.checkCalls(prog)
}

func (p *Parser) tok() Token { return p.toks[p.i] }

func (p *Parser) node(kind string, pos Pos) *Node {
	return &Node{Kind: kind, Pos: pos, Slots: map[string]*Node{}, toks: p.toks, start: p.i}
}

func later(a, b Pos) bool { return a.Line > b.Line || a.Line == b.Line && a.Col > b.Col }

func (p *Parser) fail(t Token, format string, args ...any) error {
	err := fmt.Errorf("%s: %s", t.Pos, fmt.Sprintf(format, args...))
	if !p.farHard && (p.far == nil || (t.Pos.File == p.farPos.File && later(t.Pos, p.farPos))) {
		p.far, p.farPos = err, t.Pos
	}
	return err
}

// failHard reports a definite error: the source matched a pattern, but
// breaks one of its rules (a type, a return value, an assignment target).
// If no alternative succeeds, it is reported in preference to syntax errors
// from other alternatives.
func (p *Parser) failHard(pos Pos, format string, args ...any) error {
	err := fmt.Errorf("%s: %s", pos, fmt.Sprintf(format, args...))
	if !p.farHard || (pos.File == p.farPos.File && later(pos, p.farPos)) {
		p.far, p.farPos, p.farHard = err, pos, true
	}
	return err
}

type farState struct {
	err  error
	pos  Pos
	hard bool
}

func (p *Parser) saveFar() farState     { return farState{p.far, p.farPos, p.farHard} }
func (p *Parser) restoreFar(f farState) { p.far, p.farPos, p.farHard = f.err, f.pos, f.hard }

// best returns the most informative error: the one that got furthest.
func (p *Parser) best(err error) error {
	if p.far != nil {
		return p.far
	}
	return err
}

type snapshot struct {
	i     int
	undo  int
	frame *Frame
	toks  []Token
}

func (p *Parser) save() snapshot { return snapshot{p.i, len(p.undo), p.frame, p.toks} }

func (p *Parser) restore(s snapshot) {
	p.unwind(s)
	p.i, p.toks = s.i, s.toks
}

func (p *Parser) unwind(s snapshot) {
	for len(p.undo) > s.undo {
		p.undo[len(p.undo)-1]()
		p.undo = p.undo[:len(p.undo)-1]
	}
	p.frame = s.frame
}

func (p *Parser) isLit(t Token, lit string) bool {
	return (t.Kind == TIdent || t.Kind == TPunct) && t.Text == lit
}

func (p *Parser) isCloser(t Token) bool {
	return (t.Kind == TIdent || t.Kind == TPunct) && p.L.closers[t.Text]
}

func (p *Parser) skipNL() {
	for p.tok().Kind == TNewline {
		p.i++
	}
}

func (p *Parser) parseProgram(units []Unit) (*Program, error) {
	main := newFrame("main")
	main.Ret, main.Top = "int", true
	p.frame = main
	prog := &Program{Frame: main, Types: p.types}
	var body *Node
	for _, u := range units {
		p.toks, p.i = u.Toks, 0
		if body == nil {
			body = p.node("block", p.tok().Pos)
		}
		for {
			p.skipNL()
			t := p.tok()
			if t.Kind == TEOF {
				break
			}
			if p.isCloser(t) {
				return nil, fmt.Errorf("%s: unexpected %s (nothing to close)", t.Pos, t)
			}
			n, err := p.statement(true)
			if err != nil {
				return nil, err
			}
			p.restoreFar(farState{})
			if u.Lib && n.Frame == nil && (n.Rule == nil || n.Rule.Syn.Kind != "struct" && !n.Rule.Syn.Top) {
				return nil, fmt.Errorf("%s: library code (%s) may only contain functions, structs and top-level declarations such as global and const", n.Pos, u.Name)
			}
			prog.Top = append(prog.Top, n)
			switch {
			case n.Rule != nil && n.Rule.Syn.Kind == "struct":
				prog.Structs = append(prog.Structs, n)
			case n.Frame != nil:
				prog.Funcs = append(prog.Funcs, n)
			default:
				body.Items = append(body.Items, n)
			}
		}
	}
	prog.Main = body
	prog.Globals = p.gorder
	number(prog)
	return prog, nil
}

func (p *Parser) statement(top bool) (*Node, error) {
	t := p.tok()
	var cands []*Rule
	if t.Kind == TIdent || t.Kind == TPunct {
		cands = append(cands, p.L.stmtLit[t.Text]...)
	}
	cands = append(cands, p.L.stmtSlot...)
	if len(cands) == 0 {
		return nil, p.fail(t, "%s does not start any statement the loaded packages define", t)
	}
	var first error
	far := p.saveFar()
	for _, r := range cands {
		s := p.save()
		n, err := p.match(r, top)
		if err == nil {
			if e := p.tok(); e.Kind == TNewline || e.Kind == TEOF || p.isCloser(e) {
				p.restoreFar(far)
				return n, nil
			}
			err = p.fail(p.tok(), "expected end of line, found %s", p.tok())
		}
		p.restore(s)
		if first == nil {
			first = err
		}
	}
	return nil, p.best(first)
}

func (p *Parser) match(r *Rule, top bool) (*Node, error) {
	t := p.tok()
	if (r.Syn.Kind == "function" || r.Syn.Kind == "struct" || r.Syn.Top) && !top {
		return nil, p.fail(t, "%s (%s) may only be used at the top level", t, r.Pkg.Name)
	}
	if r.Syn.InLoop && len(p.loops) == 0 {
		return nil, p.failHard(t.Pos, "%s can only be used inside a loop", t)
	}
	s := p.save()
	n := p.node(r.Kind, t.Pos)
	n.Rule = r
	if len(p.loops) > 0 {
		n.Loop = p.loops[len(p.loops)-1]
	}
	loops := p.loops
	if r.Syn.Kind == "function" {
		n.Frame = newFrame("")
		p.frame = n.Frame
		p.loops = nil
	}
	if r.Syn.Loop {
		p.loops = append(p.loops[:len(p.loops):len(p.loops)], n)
	}
	b := p.binder(n)
	err := p.elems(r.Pat.Elems, n, b)
	p.frame, p.loops = s.frame, loops
	if err != nil {
		return nil, err
	}
	n.end = p.i
	if err := p.finish(n, b, t); err != nil {
		return nil, err
	}
	if r.tmpl != nil {
		return p.expand(r, n, s)
	}
	return n, nil
}

// binder matches the type constraints of n's pattern as its slots are parsed.
func (p *Parser) binder(n *Node) *binder {
	return newBinder(func(name string) (string, bool) {
		c, ok := n.Slots[name]
		if !ok {
			return "", false
		}
		return c.Type, true
	})
}

// finish runs the checks that need the whole match: the result type,
// return statements and struct declarations.
func (p *Parser) finish(n *Node, b *binder, t Token) error {
	syn := n.Rule.Syn
	res := n.Rule.Pat.Result
	switch {
	case res != "":
		typ, err := b.Subst(res)
		if err != nil {
			return fmt.Errorf("%s: %v", n.Rule.Pkg.Origin, err)
		}
		n.Type = typ
	case syn.Kind == "expr" || syn.Kind == "infix" || syn.Kind == "prefix" || syn.Kind == "postfix":
		n.Type = "int"
	}
	if syn.Return {
		f := p.frame
		v, ok := n.Slots["value"]
		switch {
		case ok && f.Ret == "" && !f.Top:
			return p.failHard(v.Pos, "%s returns nothing, so return takes no value", f.Name)
		case ok && !Compatible(f.Ret, v.Type):
			return p.failHard(v.Pos, "%s must return %s, but this is %s", frameName(f), f.Ret, describeType(v.Type))
		case !ok && f.Ret != "" && !f.Top:
			return p.failHard(t.Pos, "%s must return %s, so return needs a value", f.Name, f.Ret)
		}
	}
	if syn.Kind == "struct" {
		return p.declareStruct(n, t)
	}
	for _, name := range n.Order {
		if c := n.Slots[name]; c.Kind == "newconst" {
			return p.declareConst(n, c)
		}
	}
	return nil
}

func frameName(f *Frame) string {
	if f.Top {
		return "the program"
	}
	return f.Name
}

func (p *Parser) elems(es []pkgfile.Elem, n *Node, b *binder) error {
	for _, e := range es {
		t := p.tok()
		switch {
		case e.Lit != "":
			if !p.isLit(t, e.Lit) {
				return p.fail(t, "expected '%s', found %s", e.Lit, t)
			}
			p.i++
		case e.Optional != nil && e.Repeat:
			// each occurrence becomes a group node in a list slot named
			// after the group's first literal
			list := p.node("groups", t.Pos)
			for p.isLit(p.tok(), e.Optional[0].Lit) {
				g := p.node("group", p.tok().Pos)
				g.Loop = n.Loop
				if err := p.elems(e.Optional, g, p.binder(g)); err != nil {
					return err
				}
				g.end = p.i
				list.Items = append(list.Items, g)
			}
			list.end = p.i
			n.setSlot(e.Optional[0].Lit, list)
		case e.Optional != nil:
			if p.isLit(t, e.Optional[0].Lit) {
				if err := p.elems(e.Optional, n, b); err != nil {
					return err
				}
			}
		case e.Slot != nil:
			start := p.i
			c, err := p.slot(e.Slot, n)
			if err != nil {
				return err
			}
			c.toks, c.start, c.end = p.toks, start, p.i
			if err := p.constrain(e.Slot, c, b, t, fmt.Sprintf("{%s} of %s", e.Slot.Name, n.Kind)); err != nil {
				return err
			}
			n.setSlot(e.Slot.Name, c)
			if err := p.typeNewvars(n, e.Slot.Name, c); err != nil {
				return err
			}
		}
	}
	return nil
}

// constrain checks a parsed slot against its pattern's type constraint.
func (p *Parser) constrain(s *pkgfile.Slot, c *Node, b *binder, t Token, what string) error {
	switch s.Kind {
	case "expr", "place":
		if c.Type == "" && s.Arg != "any" {
			return p.fail(t, "%s needs a value, but this has none", what)
		}
	case "var", "type":
	default:
		return nil
	}
	ok, err := b.Matches(s.Arg, c.Type)
	if err != nil {
		return fmt.Errorf("%s: bad type constraint %q: %v", t.Pos, s.Arg, err)
	}
	if !ok {
		want, _ := b.Subst(s.Arg)
		if want == "?" || strings.Contains(s.Arg, "[") || strings.Contains(s.Arg, "ptr") {
			want = describeConstraint(s.Arg, want)
		}
		return p.fail(t, "%s must be %s, but it is %s", what, want, describeType(c.Type))
	}
	return nil
}

func describeType(t string) string {
	if t == "" {
		return "nothing"
	}
	return t
}

func describeConstraint(arg, subst string) string {
	if !strings.Contains(subst, "?") {
		return subst
	}
	r := strings.NewReplacer("?", "…")
	return "of the form " + r.Replace(subst)
}

// typeNewvars gives a variable declared "of" the given slot its type. With
// several slots listed (of t value), the first one present wins.
func (p *Parser) typeNewvars(n *Node, slot string, c *Node) error {
	typ := c.Type
	for _, name := range n.Order {
		v := n.Slots[name]
		if v.Kind != "var" || v.Var == nil || v.Str == "" {
			continue
		}
		of := strings.Fields(v.Str)
		rank := -1
		for i, o := range of {
			if o == slot {
				rank = i
			}
		}
		if rank < 0 || (v.Int > 0 && int(v.Int)-1 < rank) {
			continue
		}
		if _, isStruct := p.types.Structs[typ]; isStruct {
			return p.failHard(c.Pos, "%s can't hold a struct value; use ptr %s", v.Var.Name, typ)
		}
		v.Int = int64(rank + 1) // remembers which "of" slot typed it
		v.Var.Type, v.Type = typ, typ
	}
	return nil
}

// fieldName reads a field name. Keywords are allowed: after '.' or in a
// struct's field list a name can't mean anything else.
func (p *Parser) fieldName() (Token, error) {
	t := p.tok()
	if t.Kind != TIdent {
		return t, p.fail(t, "expected a field name, found %s", t)
	}
	p.i++
	return t, nil
}

func (p *Parser) ident(what string) (Token, error) {
	t := p.tok()
	if t.Kind != TIdent {
		return t, p.fail(t, "expected %s, found %s", what, t)
	}
	if p.L.keywords[t.Text] {
		return t, p.fail(t, "expected %s, found keyword %s", what, t)
	}
	p.i++
	return t, nil
}

func (p *Parser) slot(s *pkgfile.Slot, parent *Node) (*Node, error) {
	t := p.tok()
	switch s.Kind {
	case "expr":
		return p.expr(0)
	case "place":
		e, err := p.expr(0)
		if err != nil {
			return nil, err
		}
		if !isPlace(e) {
			return nil, p.failHard(t.Pos, "this can't be assigned to or have its address taken")
		}
		return e, nil
	case "type":
		typ, err := p.typeExpr()
		if err != nil {
			return nil, err
		}
		n := p.node("type", t.Pos)
		n.Type = typ
		if parent.Frame != nil && s.Name == "ret" {
			parent.Frame.Ret = typ
		}
		return n, nil
	case "newtype":
		id, err := p.ident("a type name")
		if err != nil {
			return nil, err
		}
		if _, prim := p.L.Prims[id.Text]; prim || p.declared[id.Text] {
			return nil, p.fail(id, "type %s is already declared", id.Text)
		}
		p.declared[id.Text] = true
		p.undo = append(p.undo, func() { delete(p.declared, id.Text) })
		if _, ok := p.types.Structs[id.Text]; !ok {
			p.types.Structs[id.Text] = &Struct{Name: id.Text}
			p.undo = append(p.undo, func() { delete(p.types.Structs, id.Text) })
		}
		n := p.node("newtype", t.Pos)
		n.Ident = id.Text
		return n, nil
	case "fields":
		n := p.node("fields", t.Pos)
		seen := map[string]bool{}
		for {
			p.skipNL()
			if p.tok().Kind == TEOF || slices.ContainsFunc(s.Follow, func(l string) bool { return p.isLit(p.tok(), l) }) {
				return n, nil
			}
			id, err := p.fieldName()
			if err != nil {
				return nil, err
			}
			if seen[id.Text] {
				return nil, p.fail(id, "field %s is declared twice", id.Text)
			}
			seen[id.Text] = true
			if !p.isLit(p.tok(), ":") {
				return nil, p.fail(p.tok(), "expected ':' and a type after field %s", id.Text)
			}
			p.i++
			typ, err := p.typeExpr()
			if err != nil {
				return nil, err
			}
			fd := p.node("fielddecl", id.Pos)
			fd.Ident, fd.Type = id.Text, typ
			n.Items = append(n.Items, fd)
			if k := p.tok().Kind; k != TNewline && k != TEOF {
				return nil, p.fail(p.tok(), "expected end of line after field %s", id.Text)
			}
		}
	case "field":
		id, err := p.fieldName()
		if err != nil {
			return nil, err
		}
		n := p.node("field", t.Pos)
		n.Ident = id.Text
		obj, ok := parent.Slots[s.Arg]
		if !ok {
			return nil, fmt.Errorf("%s: field slot names {%s}, which comes later or is missing", t.Pos, s.Arg)
		}
		if obj.Type == "?" || (p.lenient && obj.Type == "ptr ?") {
			n.Type = "?"
			return n, nil
		}
		st := p.types.Structs[Elem(obj.Type)]
		if st == nil && p.lenient && strings.HasPrefix(obj.Type, "ptr ") {
			n.Type = "?" // a struct declared later in the file
			return n, nil
		}
		if !strings.HasPrefix(obj.Type, "ptr ") || st == nil {
			return nil, p.fail(id, "%s has no fields: it is %s, not a pointer to a struct", fieldOwner(obj), describeType(obj.Type))
		}
		f := st.Field(id.Text)
		if f == nil {
			if p.lenient && len(st.Fields) == 0 {
				n.Type = "?" // declared later in the file
				return n, nil
			}
			return nil, p.fail(id, "struct %s has no field %s", st.Name, id.Text)
		}
		n.Field, n.Type = f, f.Type
		return n, nil
	case "block":
		return p.block()
	case "newglobal":
		id, err := p.ident("a global variable name")
		if err != nil {
			return nil, err
		}
		if err := p.declareTop(id); err != nil {
			return nil, err
		}
		v := p.globals[id.Text]
		if v == nil {
			v = &Var{Name: id.Text, Type: "?", Global: true}
			p.globals[id.Text] = v
			p.undo = append(p.undo, func() { delete(p.globals, id.Text) })
		}
		p.gorder = append(p.gorder, v)
		p.undo = append(p.undo, func() { p.gorder = p.gorder[:len(p.gorder)-1] })
		n := p.node("var", t.Pos)
		n.Var, n.Type, n.Str = v, v.Type, s.Arg
		return n, nil
	case "newconst":
		id, err := p.ident("a constant name")
		if err != nil {
			return nil, err
		}
		if err := p.declareTop(id); err != nil {
			return nil, err
		}
		n := p.node("newconst", t.Pos)
		n.Ident = id.Text
		return n, nil
	case "newvar":
		id, err := p.ident("a new variable name")
		if err != nil {
			return nil, err
		}
		v, err := p.declare(id, false, "?")
		if err != nil {
			return nil, err
		}
		n := p.node("var", t.Pos)
		n.Var, n.Type, n.Str = v, v.Type, s.Arg // Str remembers the "of" slots
		return n, nil
	case "var":
		id, err := p.ident("a variable")
		if err != nil {
			return nil, err
		}
		v := p.lookup(id.Text)
		if v == nil {
			return nil, p.fail(id, "unknown variable %s", id)
		}
		n := p.node("var", t.Pos)
		n.Var, n.Type = v, v.Type
		return n, nil
	case "ident":
		id, err := p.ident("a name")
		if err != nil {
			return nil, err
		}
		n := p.node(s.Kind, t.Pos)
		n.Ident = id.Text
		return n, nil
	case "func":
		id, err := p.ident("a function name")
		if err != nil {
			return nil, err
		}
		n := p.node("func", t.Pos)
		n.Ident = id.Text
		switch sig, ok := p.sigs[id.Text]; {
		case ok:
			n.Type = sig.Ret
		case p.lenient:
			n.Type = "?"
		default:
			return nil, p.fail(id, "unknown function %s", id.Text)
		}
		return n, nil
	case "newfunc":
		id, err := p.ident("a function name")
		if err != nil {
			return nil, err
		}
		if _, dup := p.funcs[id.Text]; dup {
			return nil, p.fail(id, "function %s is already defined", id)
		}
		p.funcs[id.Text] = parent
		p.undo = append(p.undo, func() { delete(p.funcs, id.Text) })
		if parent.Frame != nil {
			parent.Frame.Name = id.Text
		}
		n := p.node("newfunc", t.Pos)
		n.Ident = id.Text
		return n, nil
	case "args":
		n := p.node("args", t.Pos)
		if p.isLit(p.tok(), ")") {
			return n, nil
		}
		for {
			e, err := p.expr(0)
			if err != nil {
				return nil, err
			}
			n.Items = append(n.Items, e)
			if !p.isLit(p.tok(), ",") {
				return n, nil
			}
			p.i++
		}
	case "params":
		n := p.node("params", t.Pos)
		if p.isLit(p.tok(), ")") {
			return n, nil
		}
		for {
			id, err := p.ident("a parameter name")
			if err != nil {
				return nil, err
			}
			if !p.isLit(p.tok(), ":") {
				return nil, p.fail(p.tok(), "parameter %s needs a type, e.g. %s: int", id.Text, id.Text)
			}
			p.i++
			typ, err := p.typeExpr()
			if err != nil {
				return nil, err
			}
			v, err := p.declare(id, true, typ)
			if err != nil {
				return nil, err
			}
			pn := p.node("var", id.Pos)
			pn.Var, pn.Type = v, v.Type
			n.Items = append(n.Items, pn)
			if !p.isLit(p.tok(), ",") {
				return n, nil
			}
			p.i++
		}
	}
	return nil, fmt.Errorf("internal: unknown slot kind %s", s.Kind)
}

func (p *Parser) declare(id Token, param bool, typ string) (*Var, error) {
	f := p.frame
	sc := f.scopes[len(f.scopes)-1]
	if _, dup := sc[id.Text]; dup {
		return nil, p.fail(id, "%s is already declared here", id)
	}
	if _, isStruct := p.types.Structs[typ]; isStruct {
		return nil, p.fail(id, "%s can't hold a struct value; use ptr %s", id.Text, typ)
	}
	v := &Var{Name: id.Text, Type: typ, Param: param}
	if param {
		v.Index = f.Params
		f.Params++
	} else {
		v.Index = f.Slots
		f.Slots++
	}
	sc[id.Text] = v
	// undoing a declaration also gives its slot back, so slot numbers
	// depend only on the final tree
	p.undo = append(p.undo, func() {
		delete(sc, id.Text)
		if param {
			f.Params--
		} else {
			f.Slots--
		}
	})
	return v, nil
}

func (p *Parser) lookup(name string) *Var {
	f := p.frame
	for i := len(f.scopes) - 1; i >= 0; i-- {
		if v, ok := f.scopes[i][name]; ok {
			return v
		}
	}
	return p.globals[name]
}

// declareTop claims a top-level name for a global or a constant.
func (p *Parser) declareTop(id Token) error {
	key := "name " + id.Text
	if p.declared[key] {
		return p.fail(id, "%s is already declared", id.Text)
	}
	p.declared[key] = true
	p.undo = append(p.undo, func() { delete(p.declared, key) })
	return nil
}

// declareConst records a constant's value, which must not depend on
// variables or calls: it is copied to every use.
func (p *Parser) declareConst(n, name *Node) error {
	v, ok := n.Slots["value"]
	if !ok {
		return fmt.Errorf("%s: a pattern with a newconst slot needs a slot named value", n.Rule.Pkg.Origin)
	}
	var bad *Node
	Walk(v, func(c *Node) {
		if bad == nil && (c.Kind == "var" || c.Kind == "func") {
			bad = c
		}
	})
	if bad != nil {
		return p.failHard(bad.Pos, "the value of constant %s must not use variables or calls", name.Ident)
	}
	old, had := p.consts[name.Ident]
	p.consts[name.Ident] = v
	p.undo = append(p.undo, func() {
		if had {
			p.consts[name.Ident] = old
		} else {
			delete(p.consts, name.Ident)
		}
	})
	return nil
}

// clone copies a constant's value for one use.
func clone(n *Node) *Node {
	c := *n
	c.Slots = map[string]*Node{}
	for k, v := range n.Slots {
		c.Slots[k] = clone(v)
	}
	c.Order = append([]string(nil), n.Order...)
	c.Items = nil
	for _, it := range n.Items {
		c.Items = append(c.Items, clone(it))
	}
	return &c
}

func (p *Parser) block() (*Node, error) {
	f := p.frame
	f.scopes = append(f.scopes, map[string]*Var{})
	defer func() { f.scopes = f.scopes[:len(f.scopes)-1] }()
	n := p.node("block", p.tok().Pos)
	for {
		p.skipNL()
		t := p.tok()
		if t.Kind == TEOF || p.isCloser(t) {
			return n, nil
		}
		s, err := p.statement(false)
		if err != nil {
			return nil, err
		}
		n.Items = append(n.Items, s)
	}
}

// ---------------------------------------------------------------- expressions

func (p *Parser) expr(minPrec int) (*Node, error) {
	start := p.i
	left, err := p.prefix()
	if err != nil {
		return nil, err
	}
	for {
		t := p.tok()
		if t.Kind != TPunct && t.Kind != TIdent {
			return left, nil
		}
		if rules := p.L.postfix[t.Text]; len(rules) > 0 && rules[0].Syn.Prec >= minPrec {
			n, err := p.postfix(rules, left, start)
			if err != nil {
				return nil, err
			}
			left = n
			continue
		}
		rules := p.L.infix[t.Text]
		if len(rules) == 0 || rules[0].Syn.Prec < minPrec {
			return left, nil
		}
		syn := rules[0].Syn
		p.i++
		next := syn.Prec + 1
		if syn.Right {
			next = syn.Prec
		}
		rt := p.tok()
		right, err := p.expr(next)
		if err != nil {
			return nil, err
		}
		// operand types choose among the operator's patterns
		var firstErr error
		var n *Node
		for _, r := range rules {
			ls, rs := r.Pat.Elems[0].Slot, r.Pat.Elems[2].Slot
			cand := p.node(r.Kind, t.Pos)
			cand.Rule, cand.start, cand.end = r, start, p.i
			b := p.binder(cand)
			err := p.constrain(ls, left, b, t, fmt.Sprintf("left side of '%s'", t.Text))
			if err == nil {
				cand.setSlot(ls.Name, left)
				err = p.constrain(rs, right, b, rt, fmt.Sprintf("right side of '%s'", t.Text))
			}
			if err == nil {
				cand.setSlot(rs.Name, right)
				err = p.finish(cand, b, t)
			}
			if err == nil {
				n = cand
				break
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		if n == nil {
			return nil, firstErr
		}
		if n.Rule.tmpl != nil {
			if left, err = p.expand(n.Rule, n, p.save()); err != nil {
				return nil, err
			}
			continue
		}
		left = n
	}
}

// postfix applies a postfix operator (field access, indexing, as) to left.
func (p *Parser) postfix(rules []*Rule, left *Node, start int) (*Node, error) {
	t := p.tok()
	var firstErr error
	far := p.saveFar()
	for _, r := range rules {
		s := p.save()
		n := p.node(r.Kind, t.Pos)
		n.Rule, n.start = r, start
		b := p.binder(n)
		ls := r.Pat.Elems[0].Slot
		err := p.constrain(ls, left, b, t, fmt.Sprintf("the left side of '%s'", t.Text))
		if err == nil {
			n.setSlot(ls.Name, left)
			err = p.elems(r.Pat.Elems[1:], n, b)
		}
		if err == nil {
			n.end = p.i
			err = p.finish(n, b, t)
		}
		if err == nil {
			p.restoreFar(far)
			if r.tmpl != nil {
				return p.expand(r, n, p.save())
			}
			return n, nil
		}
		p.restore(s)
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, p.best(firstErr)
}

func isPlace(n *Node) bool {
	return n.Kind == "var" && n.Var != nil || n.Rule != nil && n.Rule.Syn.Place
}

func fieldOwner(n *Node) string {
	if n.Kind == "var" && n.Var != nil {
		return n.Var.Name
	}
	return "this"
}

// typeExpr parses a type: a name, ptr T, or [T].
func (p *Parser) typeExpr() (string, error) {
	t := p.tok()
	switch {
	case p.isLit(t, "["):
		p.i++
		elem, err := p.typeExpr()
		if err != nil {
			return "", err
		}
		if !p.isLit(p.tok(), "]") {
			return "", p.fail(p.tok(), "expected ']' to close the slice type")
		}
		p.i++
		return "[" + elem + "]", nil
	case t.Kind == TIdent && t.Text == "ptr":
		p.i++
		elem, err := p.typeExpr()
		if err != nil {
			return "", err
		}
		return "ptr " + elem, nil
	case t.Kind == TIdent:
		p.i++
		if _, ok := p.L.Prims[t.Text]; ok {
			return t.Text, nil
		}
		if _, ok := p.types.Structs[t.Text]; ok || p.lenient {
			return t.Text, nil
		}
		return "", p.fail(t, "unknown type %s", t.Text)
	}
	return "", p.fail(t, "expected a type, found %s", t)
}

func (p *Parser) prefix() (*Node, error) {
	t := p.tok()
	start := p.i
	if t.Kind == TIdent || t.Kind == TPunct {
		if rules := p.L.prefix[t.Text]; len(rules) > 0 {
			p.i++
			ot := p.tok()
			operand, err := p.expr(rules[0].Syn.Prec)
			if err != nil {
				return nil, err
			}
			var firstErr error
			for _, r := range rules {
				sl := r.Pat.Elems[1].Slot
				n := p.node(r.Kind, t.Pos)
				n.Rule, n.start, n.end = r, start, p.i
				b := p.binder(n)
				err := p.constrain(sl, operand, b, ot, fmt.Sprintf("operand of '%s'", t.Text))
				if err == nil && sl.Kind == "place" && !isPlace(operand) {
					err = p.fail(ot, "the operand of '%s' can't have its address taken", t.Text)
				}
				if err == nil {
					n.setSlot(sl.Name, operand)
					err = p.finish(n, b, t)
				}
				if err == nil {
					if r.tmpl != nil {
						return p.expand(r, n, p.save())
					}
					return n, nil
				}
				if firstErr == nil {
					firstErr = err
				}
			}
			return nil, firstErr
		}
	}
	var cands []*Rule
	if t.Kind == TIdent || t.Kind == TPunct {
		cands = append(cands, p.L.exprLit[t.Text]...)
	}
	cands = append(cands, p.L.exprSlot...)
	far := p.saveFar()
	for _, r := range cands {
		s := p.save()
		if n, err := p.match(r, false); err == nil {
			p.restoreFar(far)
			return n, nil
		}
		p.restore(s)
	}
	ruleFar := p.saveFar()
	p.restoreFar(far) // a core form may still match
	switch t.Kind {
	case TInt, TChar:
		p.i++
		n := p.node("int", t.Pos)
		n.Int, n.Type, n.start, n.end = t.Int, "int", start, p.i
		if t.Kind == TChar {
			n.Type = "u8"
		}
		return n, nil
	case TStr:
		p.i++
		n := p.node("str", t.Pos)
		n.Str, n.Type, n.start, n.end = t.Text, "str", start, p.i
		return n, nil
	case TIdent:
		if p.L.keywords[t.Text] {
			if ruleFar.err != nil && len(p.L.exprLit[t.Text]) > 0 {
				p.restoreFar(ruleFar)
				return nil, ruleFar.err
			}
			return nil, p.fail(t, "expected an expression, found keyword %s", t)
		}
		v := p.lookup(t.Text)
		if v == nil {
			if c, ok := p.consts[t.Text]; ok {
				p.i++
				return clone(c), nil
			}
			if len(cands) > 0 && p.isLit(p.toks[p.i+1], "(") && ruleFar.err != nil {
				// looks like a use of an expression package (e.g. a call): its error is the useful one
				p.restoreFar(ruleFar)
				return nil, ruleFar.err
			}
			if p.lenient {
				// a global or constant declared later; the second pass checks it
				v = &Var{Name: t.Text, Type: "?", Global: true}
			} else {
				return nil, p.fail(t, "unknown variable %s", t)
			}
		}
		p.i++
		n := p.node("var", t.Pos)
		n.Var, n.Type, n.start, n.end = v, v.Type, start, p.i
		return n, nil
	case TPunct:
		if t.Text == "(" {
			p.i++
			e, err := p.expr(0)
			if err != nil {
				return nil, err
			}
			if !p.isLit(p.tok(), ")") {
				return nil, p.fail(p.tok(), "expected ')', found %s", p.tok())
			}
			p.i++
			return e, nil
		}
	}
	return nil, p.fail(t, "expected an expression, found %s", t)
}

// ---------------------------------------------------------------- expansion

// expand rewrites a matched node using its package's expand template: the
// slots' source tokens are pasted into the template, and the result is
// parsed again with the loaded packages.
func (p *Parser) expand(r *Rule, n *Node, s snapshot) (*Node, error) {
	if p.depth > 64 {
		return nil, fmt.Errorf("%s: expansion of %s nests too deeply (does it expand into itself?)", n.Pos, r.Pkg.Name)
	}
	end, outer := p.i, p.toks
	p.unwind(s) // declarations made while matching are redone by the expansion
	var toks []Token
	for _, t := range r.tmpl {
		if t.Kind != THole {
			toks = append(toks, t)
			continue
		}
		if name, ok := strings.CutPrefix(t.Text, "$"); ok {
			// a private name: unique to this expansion
			toks = append(toks, Token{Kind: TIdent, Pos: t.Pos,
				Text: fmt.Sprintf("__%s_%d_%d_%d", name, n.Pos.Line, n.Pos.Col, p.depth)})
			continue
		}
		c, ok := n.Slots[t.Text]
		if !ok {
			if hasSlot(r.Pat.Elems, t.Text) {
				continue // optional slot that was not given
			}
			return nil, fmt.Errorf("%s: expand template uses unknown slot {%s}", t.Pos, t.Text)
		}
		span := c.toks[c.start:c.end]
		wrap := c.Kind != "block" && len(span) > 1
		if wrap {
			toks = append(toks, Token{Kind: TPunct, Text: "(", Pos: span[0].Pos})
		}
		toks = append(toks, span...)
		if wrap {
			toks = append(toks, Token{Kind: TPunct, Text: ")", Pos: span[len(span)-1].Pos})
		}
	}
	toks = append(toks, Token{Kind: TNewline, Pos: n.Pos}, Token{Kind: TEOF, Pos: n.Pos})

	p.toks, p.i = toks, 0
	p.depth++
	defer func() { p.toks, p.i = outer, end; p.depth-- }()

	var out *Node
	switch r.Syn.Kind {
	case "statement":
		out = p.node("block", n.Pos)
		for {
			p.skipNL()
			if p.tok().Kind == TEOF {
				break
			}
			st, err := p.statement(false)
			if err != nil {
				return nil, fmt.Errorf("in expansion of %s at %s: %w", r.Pkg.Name, n.Pos, p.best(err))
			}
			out.Items = append(out.Items, st)
		}
	default:
		e, err := p.expr(0)
		if err != nil {
			return nil, fmt.Errorf("in expansion of %s at %s: %w", r.Pkg.Name, n.Pos, err)
		}
		p.skipNL()
		if p.tok().Kind != TEOF {
			return nil, fmt.Errorf("in expansion of %s at %s: template is not a single expression", r.Pkg.Name, n.Pos)
		}
		out = e
	}
	out.Expanded = append([]string{r.Pkg.Name}, out.Expanded...)
	out.toks, out.start, out.end = outer, n.start, end
	return out, nil
}

func hasSlot(es []pkgfile.Elem, name string) bool {
	for _, e := range es {
		if e.Slot != nil && e.Slot.Name == name || hasSlot(e.Optional, name) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- checks

func Walk(n *Node, fn func(*Node)) {
	if n == nil {
		return
	}
	fn(n)
	for _, c := range n.Children() {
		Walk(c, fn)
	}
}

// declareStruct lays out a struct once its fields are parsed.
func (p *Parser) declareStruct(n *Node, t Token) error {
	var name string
	var fields *Node
	for _, sname := range n.Order {
		switch c := n.Slots[sname]; c.Kind {
		case "newtype":
			name = c.Ident
		case "fields":
			fields = c
		}
	}
	if name == "" || fields == nil {
		return fmt.Errorf("%s: a struct pattern needs a newtype slot and a fields slot", n.Rule.Pkg.Origin)
	}
	st := p.types.Structs[name]
	if len(st.Fields) > 0 {
		return nil // laid out in the first pass
	}
	for _, fd := range fields.Items {
		st.Fields = append(st.Fields, &Field{Name: fd.Ident, Type: fd.Type})
	}
	if err := p.types.layout(st); err != nil {
		if p.lenient && strings.Contains(err.Error(), "unknown type") {
			// a field's type is declared later: lay it out in the second pass
			st.Fields = nil
			return nil
		}
		return p.fail(t, "%v", err)
	}
	return nil
}

// checkCalls checks every call's arguments against the function's
// parameters.
func (p *Parser) checkCalls(prog *Program) error {
	var err error
	check := func(n *Node) {
		if err != nil {
			return
		}
		for _, name := range n.Order {
			c := n.Slots[name]
			if c.Kind != "func" {
				continue
			}
			sig := p.sigs[c.Ident]
			for _, aname := range n.Order {
				a := n.Slots[aname]
				if a.Kind != "args" {
					continue
				}
				if len(a.Items) != len(sig.Params) {
					err = fmt.Errorf("%s: %s takes %d argument(s), got %d", c.Pos, c.Ident, len(sig.Params), len(a.Items))
					return
				}
				for i, arg := range a.Items {
					if !Compatible(sig.Params[i], arg.Type) {
						err = fmt.Errorf("%s: argument %d of %s must be %s, but it is %s", arg.Pos, i+1, c.Ident, sig.Params[i], describeType(arg.Type))
						return
					}
				}
			}
		}
	}
	for _, n := range prog.Top {
		Walk(n, check)
	}
	return err
}

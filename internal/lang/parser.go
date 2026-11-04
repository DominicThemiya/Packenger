package lang

import (
	"fmt"
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
	infix    map[string]*Rule
	prefix   map[string]*Rule
	closers  map[string]bool
	keywords map[string]bool
	Puncts   []string
}

var coreKinds = map[string]bool{"block": true, "int": true, "str": true, "var": true, "ident": true, "func": true, "newfunc": true, "args": true, "params": true, "program": true}

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
		infix: map[string]*Rule{}, prefix: map[string]*Rule{},
		closers: map[string]bool{}, keywords: map[string]bool{},
	}
	punct := map[string]bool{}
	for _, f := range pkgs {
		kind := f.Kind()
		if coreKinds[kind] {
			return nil, fmt.Errorf("%s: package name %q is reserved by the compiler", f.Origin, kind)
		}
		if other, ok := L.Pkgs[kind]; ok {
			return nil, fmt.Errorf("two loaded packages both define %q: %s and %s", kind, other.Name, f.Name)
		}
		L.Pkgs[kind] = f
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
				case "infix":
					op := p.Elems[1].Lit
					if o, ok := L.infix[op]; ok {
						return nil, fmt.Errorf("operator %q is defined by both %s and %s", op, o.Pkg.Name, f.Name)
					}
					L.infix[op] = r
				case "prefix":
					if o, ok := L.prefix[first.Lit]; ok {
						return nil, fmt.Errorf("prefix operator %q is defined by both %s and %s", first.Lit, o.Pkg.Name, f.Name)
					}
					L.prefix[first.Lit] = r
				case "expr":
					if first.Lit != "" {
						L.exprLit[first.Lit] = append(L.exprLit[first.Lit], r)
					} else {
						L.exprSlot = append(L.exprSlot, r)
					}
				default: // statement, function
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
	for _, m := range []map[string]*Rule{L.infix, L.prefix} {
		for _, r := range m {
			if err := L.lexTemplate(r); err != nil {
				return nil, err
			}
		}
	}
	return L, nil
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
	Name  string
	Type  string
	Param bool
	Index int
}

type Frame struct {
	Name   string
	scopes []map[string]*Var
	Slots  int
	Params int
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
	Items    []*Node
	Int      int64
	Str      string
	Ident    string
	Var      *Var
	Frame    *Frame
	Expanded []string // packages that were expanded into this node

	toks       []Token
	start, end int
}

type Program struct {
	Main  *Node // block
	Frame *Frame
	Funcs []*Node
}

// ---------------------------------------------------------------- parser

type Parser struct {
	L      *Lang
	toks   []Token
	i      int
	frame  *Frame
	funcs  map[string]*Node
	undo   []func()
	nextID int
	far    error
	farPos Pos
	depth  int
}

func NewParser(L *Lang, toks []Token) *Parser {
	return &Parser{L: L, toks: toks, funcs: map[string]*Node{}}
}

func (p *Parser) tok() Token { return p.toks[p.i] }

func (p *Parser) node(kind string, pos Pos) *Node {
	p.nextID++
	return &Node{ID: p.nextID, Kind: kind, Pos: pos, Slots: map[string]*Node{}, toks: p.toks, start: p.i}
}

func later(a, b Pos) bool { return a.Line > b.Line || a.Line == b.Line && a.Col > b.Col }

func (p *Parser) fail(t Token, format string, args ...any) error {
	err := fmt.Errorf("%s: %s", t.Pos, fmt.Sprintf(format, args...))
	if p.far == nil || (t.Pos.File == p.farPos.File && later(t.Pos, p.farPos)) {
		p.far, p.farPos = err, t.Pos
	}
	return err
}

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

func (p *Parser) ParseProgram() (*Program, error) {
	main := newFrame("main")
	p.frame = main
	prog := &Program{Frame: main}
	body := p.node("block", p.tok().Pos)
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
		p.far = nil
		if n.Frame != nil {
			prog.Funcs = append(prog.Funcs, n)
		} else {
			body.Items = append(body.Items, n)
		}
	}
	prog.Main = body
	return prog, p.checkCalls(prog)
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
	far, farPos := p.far, p.farPos
	for _, r := range cands {
		s := p.save()
		n, err := p.match(r, top)
		if err == nil {
			if e := p.tok(); e.Kind == TNewline || e.Kind == TEOF || p.isCloser(e) {
				p.far, p.farPos = far, farPos
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
	if r.Syn.Kind == "function" && !top {
		return nil, p.fail(t, "%s (%s) may only be used at the top level", t, r.Pkg.Name)
	}
	s := p.save()
	n := p.node(r.Kind, t.Pos)
	n.Rule = r
	if r.Syn.Kind == "function" {
		n.Frame = newFrame("")
		p.frame = n.Frame
	}
	err := p.elems(r.Pat.Elems, n)
	p.frame = s.frame
	if err != nil {
		return nil, err
	}
	n.end = p.i
	if err := p.setType(n); err != nil {
		return nil, err
	}
	if r.tmpl != nil {
		return p.expand(r, n, s)
	}
	return n, nil
}

func (p *Parser) setType(n *Node) error {
	res := n.Rule.Pat.Result
	if strings.HasPrefix(res, "{") {
		name := strings.Trim(res, "{}")
		c, ok := n.Slots[name]
		if !ok {
			return fmt.Errorf("%s: result type %s names a missing slot", n.Rule.Pkg.Origin, res)
		}
		n.Type = c.Type
		return nil
	}
	n.Type = res
	if n.Type == "" && (n.Rule.Syn.Kind == "expr" || n.Rule.Syn.Kind == "infix" || n.Rule.Syn.Kind == "prefix") {
		n.Type = "int"
	}
	return nil
}

func (p *Parser) elems(es []pkgfile.Elem, n *Node) error {
	for _, e := range es {
		t := p.tok()
		switch {
		case e.Lit != "":
			if !p.isLit(t, e.Lit) {
				return p.fail(t, "expected '%s', found %s", e.Lit, t)
			}
			p.i++
		case e.Optional != nil:
			if p.isLit(t, e.Optional[0].Lit) {
				if err := p.elems(e.Optional, n); err != nil {
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
			n.Slots[e.Slot.Name] = c
		}
	}
	return nil
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

func (p *Parser) checkType(t Token, want, got, what string) error {
	if want == "" || want == "any" || want == got {
		return nil
	}
	return p.fail(t, "%s must be %s, but it is %s", what, want, got)
}

func (p *Parser) slot(s *pkgfile.Slot, parent *Node) (*Node, error) {
	t := p.tok()
	switch s.Kind {
	case "expr":
		e, err := p.expr(0)
		if err != nil {
			return nil, err
		}
		if err := p.checkType(t, s.Arg, e.Type, fmt.Sprintf("{%s} of %s", s.Name, parent.Kind)); err != nil {
			return nil, err
		}
		// a newvar declared "of" this slot takes its type
		for _, other := range parent.Slots {
			if other.Kind == "var" && other.Var != nil && other.Str == s.Name {
				other.Var.Type, other.Type = e.Type, e.Type
			}
		}
		return e, nil
	case "block":
		return p.block()
	case "newvar":
		id, err := p.ident("a new variable name")
		if err != nil {
			return nil, err
		}
		v, err := p.declare(id, false)
		if err != nil {
			return nil, err
		}
		n := p.node("var", t.Pos)
		n.Var, n.Type, n.Str = v, v.Type, s.Arg // Str remembers the "of" slot
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
		if err := p.checkType(id, s.Arg, v.Type, id.Text); err != nil {
			return nil, err
		}
		n := p.node("var", t.Pos)
		n.Var, n.Type = v, v.Type
		return n, nil
	case "ident", "func":
		id, err := p.ident("a name")
		if err != nil {
			return nil, err
		}
		n := p.node(s.Kind, t.Pos)
		n.Ident = id.Text
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
			v, err := p.declare(id, true)
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

func (p *Parser) declare(id Token, param bool) (*Var, error) {
	f := p.frame
	sc := f.scopes[len(f.scopes)-1]
	if _, dup := sc[id.Text]; dup {
		return nil, p.fail(id, "%s is already declared here", id)
	}
	v := &Var{Name: id.Text, Type: "int", Param: param}
	if param {
		v.Index = f.Params
		f.Params++
	} else {
		v.Index = f.Slots
		f.Slots++
	}
	sc[id.Text] = v
	p.undo = append(p.undo, func() { delete(sc, id.Text) })
	return v, nil
}

func (p *Parser) lookup(name string) *Var {
	f := p.frame
	for i := len(f.scopes) - 1; i >= 0; i-- {
		if v, ok := f.scopes[i][name]; ok {
			return v
		}
	}
	return nil
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
		r := p.L.infix[t.Text]
		if r == nil || r.Syn.Prec < minPrec {
			return left, nil
		}
		p.i++
		next := r.Syn.Prec + 1
		if r.Syn.Right {
			next = r.Syn.Prec
		}
		rt := p.tok()
		right, err := p.expr(next)
		if err != nil {
			return nil, err
		}
		ls, rs := r.Pat.Elems[0].Slot, r.Pat.Elems[2].Slot
		if err := p.checkType(t, ls.Arg, left.Type, fmt.Sprintf("left side of '%s'", t.Text)); err != nil {
			return nil, err
		}
		if err := p.checkType(rt, rs.Arg, right.Type, fmt.Sprintf("right side of '%s'", t.Text)); err != nil {
			return nil, err
		}
		n := p.node(r.Kind, t.Pos)
		n.Rule, n.start, n.end = r, start, p.i
		n.Slots[ls.Name], n.Slots[rs.Name] = left, right
		if err := p.setType(n); err != nil {
			return nil, err
		}
		if r.tmpl != nil {
			if left, err = p.expand(r, n, p.save()); err != nil {
				return nil, err
			}
			continue
		}
		left = n
	}
}

func (p *Parser) prefix() (*Node, error) {
	t := p.tok()
	start := p.i
	if t.Kind == TIdent || t.Kind == TPunct {
		if r := p.L.prefix[t.Text]; r != nil {
			p.i++
			ot := p.tok()
			operand, err := p.expr(r.Syn.Prec)
			if err != nil {
				return nil, err
			}
			sl := r.Pat.Elems[1].Slot
			if err := p.checkType(ot, sl.Arg, operand.Type, fmt.Sprintf("operand of '%s'", t.Text)); err != nil {
				return nil, err
			}
			n := p.node(r.Kind, t.Pos)
			n.Rule, n.start, n.end = r, start, p.i
			n.Slots[sl.Name] = operand
			if err := p.setType(n); err != nil {
				return nil, err
			}
			if r.tmpl != nil {
				return p.expand(r, n, p.save())
			}
			return n, nil
		}
	}
	var cands []*Rule
	if t.Kind == TIdent || t.Kind == TPunct {
		cands = append(cands, p.L.exprLit[t.Text]...)
	}
	cands = append(cands, p.L.exprSlot...)
	far, farPos := p.far, p.farPos
	for _, r := range cands {
		s := p.save()
		if n, err := p.match(r, false); err == nil {
			p.far, p.farPos = far, farPos
			return n, nil
		}
		p.restore(s)
	}
	ruleFar, ruleFarPos := p.far, p.farPos
	p.far, p.farPos = far, farPos // a core form may still match
	switch t.Kind {
	case TInt:
		p.i++
		n := p.node("int", t.Pos)
		n.Int, n.Type, n.start, n.end = t.Int, "int", start, p.i
		return n, nil
	case TStr:
		p.i++
		n := p.node("str", t.Pos)
		n.Str, n.Type, n.start, n.end = t.Text, "str", start, p.i
		return n, nil
	case TIdent:
		if p.L.keywords[t.Text] {
			return nil, p.fail(t, "expected an expression, found keyword %s", t)
		}
		v := p.lookup(t.Text)
		if v == nil {
			if len(cands) > 0 && p.isLit(p.toks[p.i+1], "(") && ruleFar != nil {
				// looks like a use of an expression package (e.g. a call): its error is the useful one
				p.far, p.farPos = ruleFar, ruleFarPos
				return nil, ruleFar
			}
			return nil, p.fail(t, "unknown variable %s", t)
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
	for _, c := range n.Slots {
		Walk(c, fn)
	}
	for _, c := range n.Items {
		Walk(c, fn)
	}
}

func (p *Parser) checkCalls(prog *Program) error {
	var err error
	check := func(n *Node) {
		if err != nil {
			return
		}
		for _, c := range n.Slots {
			if c.Kind != "func" {
				continue
			}
			def, ok := p.funcs[c.Ident]
			if !ok {
				err = fmt.Errorf("%s: unknown function %s", c.Pos, c.Ident)
				return
			}
			for _, a := range n.Slots {
				if a.Kind == "args" && len(a.Items) != def.Frame.Params {
					err = fmt.Errorf("%s: %s takes %d argument(s), got %d", c.Pos, c.Ident, def.Frame.Params, len(a.Items))
					return
				}
			}
		}
	}
	Walk(prog.Main, check)
	for _, f := range prog.Funcs {
		Walk(f, check)
	}
	return err
}

package ide

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DominicThemiya/Packenger/internal/lang"
	"github.com/DominicThemiya/Packenger/internal/pkgfile"
)

// An Item is one completion suggestion.
type Item struct {
	Label  string `json:"label"`
	Kind   string `json:"kind"` // keyword, var, param, func, global, const, type, field, package, section, slot, hole, operator, snippet
	Detail string `json:"detail,omitempty"`
	Doc    string `json:"doc,omitempty"`
	// Insert is the text to insert, if not Label: ${name} marks a
	// placeholder, visited in order with Tab.
	Insert string `json:"insert,omitempty"`
	Rank   int    `json:"rank"` // lower first, before the match score
}

// A Completion is the answer to a completion request: Word, the partial
// word before the cursor that an item replaces, and the items.
type Completion struct {
	Word  string `json:"word"`
	Items []Item `json:"items"`
	// Hint describes what is being written: the pattern of the statement,
	// or the signature of the function being called.
	Hint string `json:"hint,omitempty"`
}

// ranks: what the cursor most likely wants comes first
const (
	rankLocal   = 10
	rankPattern = 15 // continuations of the statement being written
	rankUser    = 20 // the file's own functions, globals, constants
	rankKeyword = 30
	rankLib     = 40 // library functions
	rankOther   = 50
)

// trailingWord splits a line's text before the cursor into what comes
// before the word being typed and the word. extra are further characters
// a word may contain (/ and - in package names).
func trailingWord(prefix, extra string) (string, string) {
	i := len(prefix)
	for i > 0 {
		c := prefix[i-1]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(extra, c) >= 0 {
			i--
			continue
		}
		break
	}
	return prefix[:i], prefix[i:]
}

// inStringOrComment reports whether the end of a line's prefix is inside a
// string, a character literal or a comment.
func inStringOrComment(prefix string) bool {
	var quote byte
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		switch {
		case quote != 0 && c == '\\':
			i++
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
		case c == '"' || c == '\'':
			quote = c
		case c == '/' && i+1 < len(prefix) && prefix[i+1] == '/':
			return true
		}
	}
	return quote != 0
}

// Complete suggests what can be written at the cursor: line is 0-based and
// prefix is the line's text before the cursor.
func (e *Engine) Complete(ctx context.Context, src Source, line int, prefix string) (*Completion, error) {
	if inStringOrComment(prefix) {
		return &Completion{}, nil
	}
	lines := strings.Split(src.Text, "\n")
	if !IsProgram(src.Text) {
		section, header := sectionAt(lines, line)
		if section != "code" || header == line {
			return e.completePackage(ctx, src, lines, line, prefix, section, header)
		}
	} else if isUseLine(prefix) || strings.TrimSpace(prefix) == "use" {
		return e.completePackageName(ctx, src, prefix, "use")
	}
	v, err := e.Analyze(ctx, src, line)
	if err != nil {
		return nil, err
	}
	if v.A == nil {
		return &Completion{}, nil
	}
	return v.completeCode(lines, line, prefix), nil
}

// sectionAt finds the section of a package file that line is in, and the
// line of its header (-1 before any).
func sectionAt(lines []string, line int) (string, int) {
	section, header := "", -1
	for i := 0; i <= line && i < len(lines); i++ {
		l := lines[i]
		if l == "" || l[0] == ' ' || l[0] == '\t' {
			continue
		}
		w := strings.Fields(l)
		if len(w) == 0 {
			continue
		}
		if w[0] == "end" && section == "resolve" {
			section, header = "", i
			continue
		}
		section, header = w[0], i
	}
	return section, header
}

func (e *Engine) completePackageName(ctx context.Context, src Source, prefix, kw string) (*Completion, error) {
	_, word := trailingWord(prefix, "/-")
	out := &Completion{Word: word}
	reg := e.registry(src)
	seen := map[string]bool{}
	add := func(base string) {
		for _, n := range e.PackageNames(ctx, base) {
			if !seen[n] {
				seen[n] = true
				it := Item{Label: n, Kind: "package", Rank: rankOther + strings.Count(n, "/")}
				out.Items = append(out.Items, it)
			}
		}
	}
	add(reg.Base)
	for _, rt := range reg.Routes {
		add(rt.Base)
	}
	if kw == "use" {
		out.Items = append(out.Items, Item{Label: "pkg_tree", Kind: "package", Detail: "route packages with pkg_tree.pkg", Rank: rankKeyword})
	}
	out.Hint = kw + " <package>: a package or truck from " + reg.Base
	return out, nil
}

// ---------------------------------------------------------------- patterns

// patternText renders a pattern the way a reader thinks of it:
// for {i} = {from} to {to} {body} end.
func patternText(es []pkgfile.Elem) string {
	var parts []string
	for _, e := range es {
		switch {
		case e.Lit != "":
			parts = append(parts, e.Lit)
		case e.Slot != nil && isBlock(e):
			parts = append(parts, "…")
		case e.Slot != nil:
			parts = append(parts, "{"+e.Slot.Name+"}")
		case e.Optional != nil:
			g := "[" + patternText(e.Optional) + "]"
			if e.Repeat {
				g += "*"
			}
			parts = append(parts, g)
		}
	}
	return strings.Join(parts, " ")
}

// piece is one element of a pattern laid out as a snippet.
type piece struct {
	lit        string
	slot       *pkgfile.Slot
	afterBlock bool // a literal right after a block: it starts its own line
	beforeRep  bool // a block followed by a repeated group: it stays empty
}

// pieces flattens a pattern, including the optional group with index
// group (at the top level), or none for -1.
func pieces(es []pkgfile.Elem, group int) []piece {
	var out []piece
	var add func(es []pkgfile.Elem, top bool)
	add = func(es []pkgfile.Elem, top bool) {
		for i, e := range es {
			switch {
			case e.Lit != "":
				out = append(out, piece{lit: e.Lit, afterBlock: len(out) > 0 && out[len(out)-1].slot != nil && isBlock(pkgfile.Elem{Slot: out[len(out)-1].slot})})
			case e.Slot != nil:
				out = append(out, piece{slot: e.Slot})
			case e.Optional != nil && top && i == group:
				if len(out) > 0 && out[len(out)-1].slot != nil && e.Repeat {
					out[len(out)-1].beforeRep = true
				}
				add(e.Optional, false)
			}
		}
	}
	add(es, true)
	return out
}

// snippet lays out a pattern as text to insert, at the indentation of the
// line it goes on, with ${slot} placeholders.
func snippet(es []pkgfile.Elem, group int, indent string) string {
	var b strings.Builder
	prev := ""
	for _, p := range pieces(es, group) {
		switch {
		case p.slot != nil && isBlock(pkgfile.Elem{Slot: p.slot}):
			if !p.beforeRep {
				b.WriteString("\n" + indent + "  ${" + p.slot.Name + "}")
			}
			prev = "block"
			continue
		case p.lit != "" && (p.afterBlock || prev == "block"):
			b.WriteString("\n" + indent + p.lit)
		case p.lit == "(" || p.lit == ")" || p.lit == "," || prev == "(" || prev == "":
			b.WriteString(p.lit)
			if p.lit == "," {
				b.WriteString(" ")
			}
			if p.slot != nil {
				b.WriteString("${" + p.slot.Name + "}")
			}
		default:
			b.WriteString(" ")
			if p.slot != nil {
				b.WriteString("${" + p.slot.Name + "}")
			} else {
				b.WriteString(p.lit)
			}
		}
		if p.slot != nil {
			prev = "slot"
		} else {
			prev = p.lit
		}
	}
	return b.String()
}

// groupLabel names the variant of a pattern with optional group i.
func groupLabel(e pkgfile.Elem) string {
	if len(e.Optional) > 0 && e.Optional[0].Lit != "" {
		return e.Optional[0].Lit
	}
	return "…"
}

// forms lists the snippets of the patterns of kind statement (statement
// forms) or expr, starting with a word, for the indentation indent.
func forms(front map[string]*pkgfile.File, statements bool, indent string) []Item {
	var out []Item
	seen := map[string]bool{}
	for _, name := range sortedNames(front) {
		f := front[name]
		for _, s := range f.Syntax {
			stmt := s.Kind != "expr" && s.Kind != "infix" && s.Kind != "prefix" && s.Kind != "postfix"
			if stmt != statements || s.Kind == "infix" || s.Kind == "postfix" {
				continue
			}
			for _, p := range s.Patterns {
				first := p.Elems[0].Lit
				if first == "" || !isWordLit(first) {
					continue
				}
				variants := []int{-1}
				for i, e := range p.Elems {
					if e.Optional != nil && len(p.Elems) > 1 {
						variants = append(variants, i)
					}
				}
				for vi, g := range variants {
					ins := snippet(p.Elems, g, indent)
					if seen[ins] {
						continue
					}
					seen[ins] = true
					detail := patternText(p.Elems)
					label := first
					if g >= 0 {
						label = first + " … " + groupLabel(p.Elems[g])
					}
					out = append(out, Item{Label: label, Kind: "keyword", Detail: detail, Doc: f.Name + ": " + f.Doc, Insert: ins, Rank: rankKeyword + vi})
				}
			}
		}
	}
	return out
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------- code

// scope is what is visible at the cursor.
type scope struct {
	file   string // the analyzed file's name, as in positions
	line   int    // 1-based, in the analyzed text
	prog   *lang.Program
	front  map[string]*pkgfile.File
	locals []*lang.Var
	params map[*lang.Var]bool
	fn     *lang.Node // the function the cursor is in
	decls  map[*lang.Var]lang.Pos
}

func (v *View) scope(line int) *scope {
	s := &scope{file: filepath.Base(v.Src.Path), line: line + 1 + v.Offset, front: v.A.Front, params: map[*lang.Var]bool{}, decls: map[*lang.Var]lang.Pos{}}
	if v.A.Program == nil {
		return s
	}
	s.prog = v.A.Program
	// where each variable is declared: its first appearance
	for _, n := range s.prog.Top {
		lang.Walk(n, func(c *lang.Node) {
			if c.Kind == "var" && c.Var != nil {
				if _, ok := s.decls[c.Var]; !ok {
					s.decls[c.Var] = c.Pos
				}
			}
		})
	}
	root := s.prog.Main
	for _, f := range s.prog.Funcs {
		if a, b, ok := f.Span(); ok && a.File == s.file && a.Line <= s.line && s.line <= b.Line {
			root, s.fn = f, f
		}
	}
	seen := map[*lang.Var]bool{}
	var walk func(n *lang.Node, block *lang.Node, inParams bool)
	walk = func(n *lang.Node, block *lang.Node, inParams bool) {
		if n.Kind == "block" {
			if a, _, ok := n.Span(); ok && a.File == s.file {
				block = n
			}
		}
		if n.Kind == "params" {
			inParams = true
		}
		if n.Kind == "var" && n.Var != nil && !n.Var.Global && !seen[n.Var] {
			seen[n.Var] = true
			if n.Pos.File == s.file && n.Pos.Line <= s.line {
				visible := true
				if block != nil && !inParams {
					a, b, _ := block.Span()
					visible = a.Line <= s.line && s.line <= b.Line+1
				}
				if visible {
					s.locals = append(s.locals, n.Var)
					if inParams {
						s.params[n.Var] = true
					}
				}
			}
		}
		for _, c := range n.Children() {
			walk(c, block, inParams)
		}
	}
	walk(root, nil, false)
	return s
}

func paramsOf(f *lang.Node) []*lang.Var {
	var out []*lang.Var
	for _, name := range f.Order {
		if c := f.Slots[name]; c.Kind == "params" {
			for _, p := range c.Items {
				if p.Var != nil {
					out = append(out, p.Var)
				}
			}
		}
	}
	return out
}

// signature renders a function's signature: fib(n: int) -> int.
func signature(f *lang.Node) string {
	var ps []string
	for _, p := range paramsOf(f) {
		ps = append(ps, p.Name+": "+p.Type)
	}
	sig := f.Frame.Name + "(" + strings.Join(ps, ", ") + ")"
	if f.Frame.Ret != "" {
		sig += " -> " + f.Frame.Ret
	}
	return sig
}

// docComment returns the comment lines right above line (1-based) of lines.
func docComment(lines []string, line int) string {
	var out []string
	for i := line - 2; i >= 0 && i < len(lines); i-- {
		t := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(t, "//") {
			break
		}
		out = append([]string{strings.TrimSpace(strings.TrimPrefix(t, "//"))}, out...)
	}
	return strings.Join(out, "\n")
}

// funcDoc finds the comment above a function, in the file or its package.
func (s *scope) funcDoc(f *lang.Node, fileLines []string, offset int) (doc, where string) {
	if f.Pos.File == s.file {
		return docComment(fileLines, f.Pos.Line-offset), ""
	}
	pkg := strings.TrimSuffix(f.Pos.File, ".pkg")
	if pf := s.front[pkg]; pf != nil {
		return docComment(pf.Code, f.Pos.Line-pf.CodeAt+1), pkg
	}
	return "", pkg
}

func (s *scope) funcs() []*lang.Node {
	if s.prog == nil {
		return nil
	}
	return s.prog.Funcs
}

func (s *scope) findFunc(name string) *lang.Node {
	for _, f := range s.funcs() {
		if f.Frame.Name == name {
			return f
		}
	}
	return nil
}

func (s *scope) lookupVar(name string) *lang.Var {
	for i := len(s.locals) - 1; i >= 0; i-- {
		if s.locals[i].Name == name {
			return s.locals[i]
		}
	}
	if s.prog != nil {
		for _, g := range s.prog.Globals {
			if g.Name == name {
				return g
			}
		}
	}
	return nil
}

// consts lists the program's constants: name -> type.
func (s *scope) consts() map[string]*lang.Node {
	out := map[string]*lang.Node{}
	if s.prog == nil {
		return out
	}
	for _, n := range s.prog.Top {
		for _, name := range n.Order {
			if c := n.Slots[name]; c.Kind == "newconst" {
				out[c.Ident] = n
			}
		}
	}
	return out
}

// names lists everything an expression can refer to at the cursor.
func (v *View) names(s *scope, fileLines []string) []Item {
	var out []Item
	seen := map[string]bool{}
	for i := len(s.locals) - 1; i >= 0; i-- {
		l := s.locals[i]
		if seen[l.Name] {
			continue
		}
		seen[l.Name] = true
		kind := "var"
		if s.params[l] {
			kind = "param"
		}
		out = append(out, Item{Label: l.Name, Kind: kind, Detail: l.Type, Rank: rankLocal})
	}
	if s.prog != nil {
		for _, g := range s.prog.Globals {
			if !seen[g.Name] {
				seen[g.Name] = true
				out = append(out, Item{Label: g.Name, Kind: "global", Detail: g.Type, Rank: rankUser})
			}
		}
	}
	consts := s.consts()
	for _, name := range sortedNames(consts) {
		if !seen[name] {
			seen[name] = true
			typ := ""
			if val := consts[name].Slots["value"]; val != nil {
				typ = val.Type
			}
			out = append(out, Item{Label: name, Kind: "const", Detail: typ, Rank: rankUser})
		}
	}
	for _, f := range s.funcs() {
		name := f.Frame.Name
		if seen[name] {
			continue
		}
		seen[name] = true
		doc, where := s.funcDoc(f, fileLines, v.Offset)
		rank := rankUser
		if where != "" {
			rank = rankLib
			doc = strings.TrimSpace(doc + "\n\n" + where)
		}
		var args []string
		for _, p := range paramsOf(f) {
			args = append(args, "${"+p.Name+"}")
		}
		out = append(out, Item{Label: name, Kind: "func", Detail: signature(f), Doc: doc, Insert: name + "(" + strings.Join(args, ", ") + ")", Rank: rank})
	}
	return out
}

// types lists the type names: primitives, structs and the type syntax.
func (s *scope) types(L *lang.Lang) []Item {
	var out []Item
	prims := map[string]bool{}
	for _, f := range s.front {
		for _, t := range f.Types {
			prims[t.Name] = true
		}
	}
	for _, p := range sortedNames(prims) {
		out = append(out, Item{Label: p, Kind: "type", Detail: "primitive", Rank: rankKeyword})
	}
	if s.prog != nil {
		for _, name := range s.prog.Types.StructNames() {
			st := s.prog.Types.Structs[name]
			var fs []string
			for _, f := range st.Fields {
				fs = append(fs, f.Name+": "+f.Type)
			}
			out = append(out, Item{Label: name, Kind: "struct", Detail: "struct", Doc: strings.Join(fs, "\n"), Rank: rankUser})
		}
	}
	out = append(out,
		Item{Label: "ptr", Kind: "keyword", Detail: "ptr T: a pointer", Insert: "ptr ${T}", Rank: rankKeyword},
		Item{Label: "[T]", Kind: "keyword", Detail: "a slice", Insert: "[${T}]", Rank: rankKeyword},
		Item{Label: "fn", Kind: "keyword", Detail: "fn(A, B) -> R: a function type", Insert: "fn(${params}) -> ${R}", Rank: rankKeyword})
	return out
}

// exprType works out the type of the expression toks, for field access:
// names, fields, indexing, calls, parentheses and as.
func (s *scope) exprType(toks []lang.Token) string {
	n := len(toks)
	if n == 0 {
		return ""
	}
	last := toks[n-1]
	match := func(open, close string) int {
		depth := 0
		for i := n - 1; i >= 0; i-- {
			switch toks[i].Text {
			case close:
				depth++
			case open:
				depth--
				if depth == 0 {
					return i
				}
			}
		}
		return -1
	}
	switch {
	case last.Kind == lang.TPunct && last.Text == "]":
		if i := match("[", "]"); i > 0 {
			return lang.Elem(s.exprType(toks[:i]))
		}
	case last.Kind == lang.TPunct && last.Text == ")":
		i := match("(", ")")
		if i < 0 {
			return ""
		}
		if i > 0 && toks[i-1].Kind == lang.TIdent && (i < 2 || toks[i-2].Text != ".") {
			if f := s.findFunc(toks[i-1].Text); f != nil {
				return f.Frame.Ret
			}
			return ""
		}
		inner := toks[i+1 : n-1]
		depth := 0
		for k, t := range inner {
			switch t.Text {
			case "(", "[":
				depth++
			case ")", "]":
				depth--
			case "as":
				if depth == 0 {
					var ws []string
					for _, w := range inner[k+1:] {
						ws = append(ws, w.Text)
					}
					return strings.Join(ws, " ")
				}
			}
		}
		return s.exprType(inner)
	case last.Kind == lang.TIdent:
		if n >= 2 && toks[n-2].Text == "." {
			if f := s.field(s.exprType(toks[:n-2]), last.Text); f != nil {
				return f.Type
			}
			return ""
		}
		if v := s.lookupVar(last.Text); v != nil {
			return v.Type
		}
		if c := s.consts()[last.Text]; c != nil && c.Slots["value"] != nil {
			return c.Slots["value"].Type
		}
	}
	return ""
}

func (s *scope) structOf(typ string) *lang.Struct {
	if s.prog == nil {
		return nil
	}
	return s.prog.Types.Structs[strings.TrimPrefix(typ, "ptr ")]
}

func (s *scope) field(typ, name string) *lang.Field {
	if st := s.structOf(typ); st != nil {
		return st.Field(name)
	}
	return nil
}

// openBlock finds the innermost block open at line (0-based) of lines: the
// word that opened it, or "".
func openBlock(g *grammarInfo, lines []string, line int) string {
	var stack []string
	for i := 0; i < line && i < len(lines); i++ {
		f := strings.Fields(strings.SplitN(lines[i], "//", 2)[0])
		if len(f) == 0 {
			continue
		}
		w := f[0]
		switch {
		case g.closers[w] && (len(f) == 1 || g.args[w]):
			if !g.reopen[w] && len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case g.openers[w] && !(len(f) > 1 && f[1] == "="):
			stack = append(stack, w)
		}
	}
	if len(stack) == 0 {
		return ""
	}
	return stack[len(stack)-1]
}

// closersFor lists the words that may continue or end the block opened by
// opener (else, elif, end for if), with their snippets.
func closersFor(front map[string]*pkgfile.File, opener, indent string) []Item {
	var out []Item
	seen := map[string]bool{}
	for _, name := range sortedNames(front) {
		f := front[name]
		for _, s := range f.Syntax {
			for _, p := range s.Patterns {
				if p.Elems[0].Lit != opener {
					continue
				}
				var walk func(es []pkgfile.Elem)
				walk = func(es []pkgfile.Elem) {
					for _, e := range es {
						if e.Optional != nil {
							walk(e.Optional)
						}
						if !isBlock(e) {
							continue
						}
						for _, c := range e.Slot.Follow {
							if seen[c] {
								continue
							}
							seen[c] = true
							ins := c
							// the rest of the clause: elif {cond}, case {v}
							if rest := clauseAfter(p.Elems, c); rest != "" {
								ins = c + " " + rest
							}
							out = append(out, Item{Label: c, Kind: "keyword", Detail: "in " + opener + " … " + patternText(p.Elems), Insert: ins, Rank: rankPattern})
						}
					}
				}
				walk(p.Elems)
			}
		}
	}
	return out
}

// clauseAfter renders the slots that follow literal c on its line in a
// pattern: elif {cond} gives "${cond}".
func clauseAfter(es []pkgfile.Elem, c string) string {
	var find func(es []pkgfile.Elem) []pkgfile.Elem
	find = func(es []pkgfile.Elem) []pkgfile.Elem {
		for i, e := range es {
			if e.Lit == c {
				return es[i+1:]
			}
			if e.Optional != nil {
				if r := find(e.Optional); r != nil {
					return r
				}
			}
		}
		return nil
	}
	var parts []string
	for _, e := range find(es) {
		if e.Slot == nil || isBlock(e) {
			break
		}
		parts = append(parts, "${"+e.Slot.Name+"}")
	}
	return strings.Join(parts, " ")
}

// continuations suggests the literals that come next in the statement
// being written, from the patterns starting with its first word: = and to
// after "for i", -> after "fn f(x: int)".
func continuations(front map[string]*pkgfile.File, toks []lang.Token) []Item {
	if len(toks) == 0 || toks[0].Kind != lang.TIdent {
		return nil
	}
	type lit struct {
		text       string
		optional   bool
		afterBlock bool
	}
	var out []Item
	seen := map[string]bool{}
	for _, name := range sortedNames(front) {
		for _, s := range front[name].Syntax {
			for _, p := range s.Patterns {
				if p.Elems[0].Lit != toks[0].Text {
					continue
				}
				var seq []lit
				var flat func(es []pkgfile.Elem, optional bool)
				flat = func(es []pkgfile.Elem, optional bool) {
					prevBlock := false
					for _, e := range es {
						switch {
						case e.Lit != "":
							seq = append(seq, lit{e.Lit, optional, prevBlock})
						case e.Optional != nil:
							flat(e.Optional, true)
						}
						prevBlock = isBlock(e)
					}
				}
				flat(p.Elems[1:], false)
				k := 0
				for _, t := range toks[1:] {
					for m := k; m < len(seq); m++ {
						if seq[m].text == t.Text {
							k = m + 1
							break
						}
					}
				}
				for m := k; m < len(seq) && !seq[m].afterBlock; m++ {
					if !seen[seq[m].text] {
						seen[seq[m].text] = true
						out = append(out, Item{Label: seq[m].text, Kind: "operator", Detail: patternText(p.Elems), Doc: name + ": " + front[name].Doc, Rank: rankPattern})
					}
					if !seq[m].optional {
						break
					}
				}
			}
		}
	}
	return out
}

// statementHint describes the patterns the line's first word starts.
func statementHint(front map[string]*pkgfile.File, word string) string {
	var hs []string
	for _, name := range sortedNames(front) {
		for _, s := range front[name].Syntax {
			for _, p := range s.Patterns {
				if p.Elems[0].Lit == word {
					hs = append(hs, patternText(p.Elems)+"   — "+name)
				}
			}
		}
	}
	return strings.Join(hs, "\n")
}

// callHint finds the function call the cursor is in, and describes it with
// the argument being written marked.
func (s *scope) callHint(toks []lang.Token) string {
	depth, commas := 0, 0
	for i := len(toks) - 1; i >= 0; i-- {
		switch toks[i].Text {
		case ")", "]":
			depth++
		case "[":
			depth--
		case ",":
			if depth == 0 {
				commas++
			}
		case "(":
			if depth > 0 {
				depth--
				continue
			}
			if i > 0 && toks[i-1].Kind == lang.TIdent {
				if f := s.findFunc(toks[i-1].Text); f != nil {
					ps := paramsOf(f)
					var parts []string
					for k, p := range ps {
						a := p.Name + ": " + p.Type
						if k == commas {
							a = "‹" + a + "›"
						}
						parts = append(parts, a)
					}
					h := f.Frame.Name + "(" + strings.Join(parts, ", ") + ")"
					if f.Frame.Ret != "" {
						h += " -> " + f.Frame.Ret
					}
					return h
				}
			}
			return ""
		}
	}
	return ""
}

// operandEnd reports whether a token ends an operand, so that what follows
// is an operator: a name, a literal, ) or ].
func operandEnd(L *lang.Lang, s *scope, t lang.Token) bool {
	switch t.Kind {
	case lang.TInt, lang.TStr, lang.TChar:
		return true
	case lang.TPunct:
		return t.Text == ")" || t.Text == "]"
	case lang.TIdent:
		kw := false
		for _, k := range L.Keywords() {
			if k == t.Text {
				kw = true
				break
			}
		}
		return !kw || s.lookupVar(t.Text) != nil
	}
	return false
}

func (v *View) completeCode(fileLines []string, line int, prefix string) *Completion {
	L := v.A.Lang
	before, word := trailingWord(prefix, "")
	out := &Completion{Word: word}
	toks, err := lang.Lex(before, "x", L.Puncts, false)
	if err != nil {
		return out
	}
	for len(toks) > 0 && (toks[len(toks)-1].Kind == lang.TEOF || toks[len(toks)-1].Kind == lang.TNewline) {
		toks = toks[:len(toks)-1]
	}
	s := v.scope(line)
	indent := prefix[:len(prefix)-len(strings.TrimLeft(prefix, " \t"))]
	if h := s.callHint(toks); h != "" {
		out.Hint = h
	} else if len(toks) > 0 && toks[0].Kind == lang.TIdent {
		out.Hint = statementHint(v.A.Front, toks[0].Text)
	}
	var last lang.Token
	if len(toks) > 0 {
		last = toks[len(toks)-1]
	}
	switch {
	case len(toks) > 0 && last.Kind == lang.TPunct && last.Text == ".":
		typ := s.exprType(toks[:len(toks)-1])
		if st := s.structOf(typ); st != nil {
			for i, f := range st.Fields {
				out.Items = append(out.Items, Item{Label: f.Name, Kind: "field", Detail: f.Type, Doc: st.Name + "." + f.Name, Rank: rankLocal + i})
			}
		}
		return out
	case len(toks) > 0 && (last.Text == ":" || last.Text == "->" || last.Text == "as" || last.Text == "new" || last.Text == "ptr" ||
		last.Text == "[" && len(toks) > 1 && (toks[len(toks)-2].Text == ":" || toks[len(toks)-2].Text == "(" || toks[len(toks)-2].Text == "->")):
		out.Items = s.types(L)
		return out
	case len(toks) == 0:
		// the start of a statement
		lines := fileLines
		if v.Offset != 0 || !v.Program {
			lines = codeLines(fileLines, v)
		}
		if op := openBlock(blockInfo(v.A.Front), lines, line); op != "" {
			out.Items = append(out.Items, closersFor(v.A.Front, op, indent)...)
		}
		out.Items = append(out.Items, forms(v.A.Front, true, indent)...)
		out.Items = append(out.Items, v.names(s, fileLines)...)
		out.Items = append(out.Items, forms(v.A.Front, false, indent)...)
		return out
	case operandEnd(L, s, last):
		out.Items = append(out.Items, continuations(v.A.Front, toks)...)
		for _, name := range sortedNames(v.A.Front) {
			for _, sy := range v.A.Front[name].Syntax {
				if sy.Kind != "infix" && sy.Kind != "postfix" {
					continue
				}
				for _, p := range sy.Patterns {
					if len(p.Elems) > 1 && isWordLit(p.Elems[1].Lit) {
						out.Items = append(out.Items, Item{Label: p.Elems[1].Lit, Kind: "operator", Detail: patternText(p.Elems), Doc: name + ": " + v.A.Front[name].Doc, Rank: rankKeyword})
					}
				}
			}
		}
		return out
	}
	out.Items = append(out.Items, continuations(v.A.Front, toks)...)
	out.Items = append(out.Items, v.names(s, fileLines)...)
	out.Items = append(out.Items, forms(v.A.Front, false, indent)...)
	return out
}

// codeLines is the file as the analysis saw it: for a package file, its
// code section only.
func codeLines(fileLines []string, v *View) []string {
	if v.Program {
		return fileLines
	}
	section := ""
	out := make([]string, len(fileLines))
	for i, l := range fileLines {
		if l != "" && l[0] != ' ' && l[0] != '\t' {
			section = strings.Fields(l)[0]
			continue
		}
		if section == "code" {
			out[i] = l
		}
	}
	return out
}

// ---------------------------------------------------------------- package files

var slotKindDocs = map[string]string{
	"expr":      "an expression; expr T requires type T",
	"place":     "something assignable: a variable, field, element or *p",
	"block":     "lines of statements, up to the next literal",
	"newvar":    "a new variable; newvar of slot types it like that slot",
	"var":       "an existing variable",
	"ident":     "a name",
	"func":      "an existing function's name",
	"newfunc":   "a new function's name",
	"args":      "a comma-separated argument list",
	"params":    "a parameter list: name: type, ...",
	"type":      "a type: int, ptr T, [T], fn(A) -> R",
	"field":     "a field name; field of slot looks it up in that slot's struct",
	"newtype":   "a new struct's name",
	"fields":    "struct field lines, up to the next literal",
	"newglobal": "a new global variable",
	"newconst":  "a new constant",
}

var sectionDocs = map[string][2]string{
	"doc":     {"doc ${summary}\n  ${body}", "the one-line summary, then Markdown indented by two spaces"},
	"needs":   {"needs ${packages}", "packages this one uses: their syntax, code and backend"},
	"type":    {"type ${name} ${size}", "a primitive type and its size in bytes"},
	"truck":   {"truck\n  ${member}", "a truck: its members, one per line, in the folder of the same name"},
	"syntax":  {"syntax ${statement}\n  ${pattern}", "patterns: statement, function, struct, expr, infix N, prefix N or postfix N"},
	"expand":  {"expand\n  ${template}", "rewrite matches into other packages' syntax, with {slot} holes"},
	"code":    {"code\n  ${source}", "library code: functions, structs, globals and constants"},
	"emit":    {"emit\n  ${template}", "a backend template; emit <section> [when <condition>]"},
	"data":    {"data\n  ${lines}", "lines a backend's program skeleton places in the data section"},
	"support": {"support\n  ${lines}", "code a backend includes once, when used"},
	"resolve": {"resolve\n  rename ${kind} '${old}' in ${scope} to '${new}'\nend", "rename what other packages define, to resolve conflicts"},
}

func (e *Engine) completePackage(ctx context.Context, src Source, lines []string, line int, prefix, section string, header int) (*Completion, error) {
	trimmed := strings.TrimLeft(prefix, " \t")
	indented := len(trimmed) < len(prefix)
	before, word := trailingWord(prefix, "")
	out := &Completion{Word: word}
	if !indented {
		fields := strings.Fields(prefix)
		switch {
		case len(fields) == 0 || len(fields) == 1 && word != "" && !strings.HasSuffix(prefix, " "):
			for i, k := range sectionKeywords {
				d := sectionDocs[k]
				out.Items = append(out.Items, Item{Label: k, Kind: "section", Detail: d[1], Insert: d[0], Rank: rankKeyword + i})
			}
			if section == "resolve" {
				out.Items = append(out.Items, Item{Label: "end", Kind: "keyword", Detail: "close the resolve section", Rank: rankPattern})
			}
		case fields[0] == "needs":
			return e.completePackageName(ctx, src, prefix, "needs")
		case fields[0] == "syntax":
			if len(fields) == 1 || len(fields) == 2 && word != "" {
				for _, k := range []string{"statement", "function", "struct", "expr", "infix", "prefix", "postfix"} {
					out.Items = append(out.Items, Item{Label: k, Kind: "keyword", Detail: "syntax kind", Rank: rankKeyword})
				}
			} else {
				for _, k := range []string{"left", "right", "place", "return", "loop", "inloop", "top"} {
					out.Items = append(out.Items, Item{Label: k, Kind: "keyword", Detail: "syntax flag", Rank: rankKeyword})
				}
			}
			out.Hint = "syntax <statement|function|struct|expr|infix N|prefix N|postfix N> [left|right] [place] [return] [loop] [inloop] [top]"
		case fields[0] == "emit":
			if strings.Contains(prefix, " when ") {
				for _, k := range []string{"is", "has", "size", "count", "simple", "empty", "missing", "int", "u8", "bool", "str"} {
					out.Items = append(out.Items, Item{Label: k, Kind: "keyword", Detail: "condition", Rank: rankKeyword})
				}
			} else {
				out.Items = append(out.Items, Item{Label: "when", Kind: "keyword", Detail: "emit [section] when <condition>", Insert: "when ${slot} ${condition}", Rank: rankKeyword})
			}
			out.Hint = "emit [section] [when <slot> <type> | <slot> is <kind> | <slot> has <section> | <slot> size <n> | <slot> count <n> | <slot> simple | empty | missing]"
		}
		return out, nil
	}
	pf, _ := pkgfile.Parse(pkgName(src.Path), src.Path, src.Text)
	switch section {
	case "truck":
		base := strings.TrimSuffix(src.Path, ".pkg")
		listed := map[string]bool{}
		for i := header + 1; i < len(lines); i++ {
			if t := strings.TrimSpace(lines[i]); t != "" {
				listed[t] = true
			}
		}
		entries, _ := os.ReadDir(base)
		for _, en := range entries {
			n := strings.TrimSuffix(en.Name(), ".pkg")
			if strings.HasSuffix(en.Name(), ".pkg") && !listed[n] {
				out.Items = append(out.Items, Item{Label: n, Kind: "package", Detail: "member file " + filepath.Base(base) + "/" + en.Name(), Rank: rankLocal})
			}
		}
	case "syntax":
		out.Items, out.Hint = syntaxItems(before, word)
	case "expand", "emit":
		if open := strings.LastIndex(before, "{"); open > strings.LastIndex(before, "}") {
			out.Items = holeItems(pf, before[open+1:], section)
			return out, nil
		}
		if section == "emit" && strings.HasSuffix(before, "@") {
			dir := filepath.Dir(src.Path)
			entries, _ := os.ReadDir(dir)
			for _, en := range entries {
				if strings.HasSuffix(en.Name(), ".pkg") {
					out.Items = append(out.Items, Item{Label: strings.TrimSuffix(en.Name(), ".pkg"), Kind: "package", Detail: "include a package of this backend", Rank: rankLocal})
				}
			}
			return out, nil
		}
		if needs := needsOf(lines); section == "expand" && len(needs) > 0 {
			// expand templates are written in the syntax of what the
			// package needs
			text := "use " + strings.Join(needs, "\nuse ") + "\n"
			if a, err := e.analyzeText(ctx, src, text); err == nil {
				ind := before[:len(before)-len(strings.TrimLeft(before, " \t"))]
				if strings.TrimSpace(before) == "" {
					out.Items = append(out.Items, forms(a.Front, true, ind)...)
				}
				out.Items = append(out.Items, forms(a.Front, false, ind)...)
			}
		}
		out.Items = append(out.Items, Item{Label: "{slot}", Kind: "hole", Detail: "a slot's source", Insert: "{${slot}}", Rank: rankPattern})
	case "resolve":
		f := strings.Fields(before)
		switch {
		case len(f) == 0:
			out.Items = append(out.Items, Item{Label: "rename", Kind: "snippet", Detail: "rename <kind> '<old>' in <scope> to '<new>'", Insert: "rename ${kind} '${old}' in ${scope} to '${new}'", Rank: rankKeyword})
		case len(f) == 1 && f[0] == "rename":
			for _, k := range []string{"prefix", "name", "keyword", "symbol"} {
				out.Items = append(out.Items, Item{Label: k, Kind: "keyword", Detail: "resolve/" + k, Rank: rankKeyword})
			}
		case f[len(f)-1] == "in":
			for _, n := range needsOf(lines) {
				out.Items = append(out.Items, Item{Label: n, Kind: "package", Detail: "a package or truck this one needs", Rank: rankLocal})
			}
		case len(f) == 3:
			out.Items = append(out.Items, Item{Label: "in", Kind: "keyword", Rank: rankKeyword})
		case len(f) == 5:
			out.Items = append(out.Items, Item{Label: "to", Kind: "keyword", Rank: rankKeyword})
		}
		out.Hint = "rename prefix|name|keyword|symbol '<old>' in <package or truck> to '<new>'"
	}
	return out, nil
}

// needsOf reads a package file's needs lines, even if it doesn't parse.
func needsOf(lines []string) []string {
	var out []string
	for _, l := range lines {
		if f := strings.Fields(l); len(f) > 0 && f[0] == "needs" && l[0] == 'n' {
			out = append(out, f[1:]...)
		}
	}
	return out
}

// syntaxItems completes a pattern line of a syntax section.
func syntaxItems(before, word string) ([]Item, string) {
	open := strings.LastIndex(before, "{")
	if open > strings.LastIndex(before, "}") {
		inside := before[open+1:]
		colon := strings.Index(inside, ":")
		switch {
		case colon < 0:
			var out []Item
			for _, n := range []string{"cond", "body", "value", "name", "left", "right", "x", "args", "params", "t"} {
				out = append(out, Item{Label: n, Kind: "slot", Detail: "a slot name", Insert: n + ": ${kind}", Rank: rankOther})
			}
			return out, "{name: kind [constraint]}"
		default:
			rest := strings.Fields(inside[colon+1:])
			if len(rest) == 0 || len(rest) == 1 && word != "" {
				var out []Item
				for _, k := range sortedNames(slotKindDocs) {
					out = append(out, Item{Label: k, Kind: "slot", Detail: slotKindDocs[k], Rank: rankKeyword})
				}
				return out, "{name: kind [constraint]}: " + strings.Join(sortedNames(slotKindDocs), " ")
			}
			kind := rest[0]
			if kind == "newvar" || kind == "field" {
				return []Item{{Label: "of", Kind: "keyword", Detail: kind + " of <slot>", Insert: "of ${slot}", Rank: rankKeyword}}, kind + " of <slot>[] ..."
			}
			var out []Item
			for _, t := range []string{"int", "u8", "bool", "str", "any", "T", "ptr T", "ptr ?", "[T]", "fn(...) -> R"} {
				out = append(out, Item{Label: t, Kind: "type", Detail: "type constraint", Rank: rankKeyword})
			}
			return out, "{name: " + kind + " <type>}: T and ? match any type"
		}
	}
	if strings.HasSuffix(strings.TrimSpace(before), "->") {
		var out []Item
		for _, t := range []string{"int", "u8", "bool", "str", "T", "ptr T", "{slot}"} {
			out = append(out, Item{Label: t, Kind: "type", Detail: "result type", Rank: rankKeyword})
		}
		return out, "-> <type> or {slot}: the type of the expression"
	}
	return []Item{
		{Label: "{name: expr}", Kind: "slot", Detail: "an expression slot", Insert: "{${name}: expr}", Rank: rankKeyword},
		{Label: "{body: block}", Kind: "slot", Detail: "a block slot", Insert: "{${body}: block}", Rank: rankKeyword},
		{Label: "[optional]", Kind: "snippet", Detail: "an optional group, starting with a literal", Insert: "[${literal}]", Rank: rankKeyword},
		{Label: "[repeated]*", Kind: "snippet", Detail: "a group repeated any number of times", Insert: "[${literal}]*", Rank: rankKeyword},
		{Label: "-> type", Kind: "snippet", Detail: "the result type of an expression", Insert: "-> ${type}", Rank: rankKeyword},
	}, "literals, {slots}, [optional groups] and -> result"
}

// holeItems completes a {hole} in an expand or emit template.
func holeItems(pf *pkgfile.File, inside, section string) []Item {
	var out []Item
	seen := map[string]bool{}
	if pf != nil {
		for _, s := range pf.Syntax {
			for _, p := range s.Patterns {
				var walk func(es []pkgfile.Elem)
				walk = func(es []pkgfile.Elem) {
					for _, e := range es {
						if e.Slot != nil && !seen[e.Slot.Name] {
							seen[e.Slot.Name] = true
							out = append(out, Item{Label: e.Slot.Name, Kind: "hole", Detail: e.Slot.Kind + " " + e.Slot.Arg, Rank: rankLocal})
						}
						if e.Optional != nil {
							walk(e.Optional)
						}
					}
				}
				walk(p.Elems)
			}
		}
	}
	if section == "expand" {
		out = append(out,
			Item{Label: "each", Kind: "keyword", Detail: "{each x in list} … {end}: once per item of a repeated group", Insert: "each ${x} in ${list}", Rank: rankKeyword},
			Item{Label: "end", Kind: "keyword", Detail: "ends {each}", Rank: rankKeyword},
			Item{Label: "@pos", Kind: "keyword", Detail: "the position of the match, as a string", Rank: rankKeyword},
			Item{Label: "$name", Kind: "keyword", Detail: "a private name, unique to this expansion", Insert: "$${name}", Rank: rankKeyword})
	}
	return out
}

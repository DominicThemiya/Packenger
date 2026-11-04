// Package pkgfile parses Packenger package files.
//
// A package file is a list of sections. A section header starts in column 1;
// its body lines are indented. Lines starting with "//" are comments.
//
//	doc      <one-line summary>
//	  <Markdown body, indented two spaces; blank lines and // are kept>
//	needs    <package> <package> ...        other packages this one builds on
//	truck                                   this file is a truck (a collection);
//	  <member>                              members live at <truck>/<member>.pkg
//	syntax   <kind> [precedence] [left|right]
//	  <pattern> [-> <type> | -> {slot}]     one or more patterns
//	expand                                  rewrite into other packages' syntax
//	  <source with {slot} holes>
//	emit     [<section>] [when <condition>] backend code (NASM) for this package
//	  <asm with {placeholders}>
//	data                                    per-use lines for the data section
//	support                                 lines emitted once if the package is used
//
// Pattern elements are literals (if, end, +, ...), slots {name: kind [arg]}
// and optional groups [ ... ].
package pkgfile

import (
	"fmt"
	"strconv"
	"strings"
)

type File struct {
	Name    string // e.g. "std/if"
	Origin  string
	Doc     string   // one-line summary
	DocBody []string // Markdown, dedented
	Needs   []string
	Truck   []string
	IsTruck bool
	Syntax  []*Syntax
	Expand  []string
	Emits   []*Emit
	Data    []string
	Support []string
}

// Kind is the node kind this package defines: the last path segment.
func (f *File) Kind() string { return Kind(f.Name) }

func Kind(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}

type Syntax struct {
	Kind     string // statement, function, expr, infix, prefix
	Prec     int
	Right    bool
	Patterns []*Pattern
	Line     int
}

type Pattern struct {
	Elems  []Elem
	Result string // type name, "{slot}" (type of that slot), or ""
	Line   int
}

type Elem struct {
	Lit      string
	Slot     *Slot
	Optional []Elem
}

type Slot struct {
	Name string
	Kind string // expr block newvar var ident func newfunc args params
	Arg  string // expr/var: required type; newvar: "of <slot>"
}

type Emit struct {
	Section string // "" = default
	Cond    *Cond
	Lines   []string
	Line    int
}

type Cond struct {
	Slot string
	Op   string // type, is, has, simple, empty
	Arg  string
}

var sectionKeywords = map[string]bool{
	"doc": true, "needs": true, "truck": true, "syntax": true, "expand": true,
	"emit": true, "data": true, "support": true,
}

var slotKinds = map[string]bool{
	"expr": true, "block": true, "newvar": true, "var": true, "ident": true,
	"func": true, "newfunc": true, "args": true, "params": true,
}

var syntaxKinds = map[string]bool{
	"statement": true, "function": true, "expr": true, "infix": true, "prefix": true,
}

func Parse(name, origin, src string) (*File, error) {
	f := &File{Name: name, Origin: origin}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	errf := func(line int, format string, a ...any) error {
		return fmt.Errorf("%s:%d: %s", origin, line, fmt.Sprintf(format, a...))
	}
	var cur *[]string // where body lines go
	var curSyntax *Syntax
	inDoc := false
	for idx, raw := range lines {
		ln := idx + 1
		trimmed := strings.TrimSpace(raw)
		if inDoc && (raw == "" || raw[0] == ' ' || raw[0] == '\t') {
			// doc bodies are Markdown: keep blank lines, comments and indentation
			if trimmed == "" {
				f.DocBody = append(f.DocBody, "")
			} else {
				f.DocBody = append(f.DocBody, dedent(raw))
			}
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if raw[0] != ' ' && raw[0] != '\t' {
			fields := strings.Fields(trimmed)
			kw := fields[0]
			if !sectionKeywords[kw] {
				return nil, errf(ln, "unknown section %q (expected one of doc, needs, truck, syntax, expand, emit, data, support)", kw)
			}
			cur, curSyntax = nil, nil
			inDoc = kw == "doc"
			args := fields[1:]
			switch kw {
			case "doc":
				f.Doc = strings.TrimSpace(strings.TrimPrefix(trimmed, "doc"))
			case "needs":
				f.Needs = append(f.Needs, args...)
			case "truck":
				f.IsTruck = true
				cur = &f.Truck
			case "syntax":
				s, err := parseSyntaxHeader(args)
				if err != nil {
					return nil, errf(ln, "%v", err)
				}
				s.Line = ln
				f.Syntax = append(f.Syntax, s)
				curSyntax = s
			case "expand":
				if f.Expand != nil {
					return nil, errf(ln, "only one expand section is allowed")
				}
				f.Expand = []string{}
				cur = &f.Expand
			case "emit":
				e, err := parseEmitHeader(args)
				if err != nil {
					return nil, errf(ln, "%v", err)
				}
				e.Line = ln
				f.Emits = append(f.Emits, e)
				cur = &e.Lines
			case "data":
				cur = &f.Data
			case "support":
				cur = &f.Support
			}
			continue
		}
		switch {
		case curSyntax != nil:
			p, err := ParsePattern(trimmed)
			if err != nil {
				return nil, errf(ln, "%v", err)
			}
			p.Line = ln
			if err := checkPattern(curSyntax, p); err != nil {
				return nil, errf(ln, "%v", err)
			}
			curSyntax.Patterns = append(curSyntax.Patterns, p)
		case cur != nil:
			*cur = append(*cur, trimmed)
		default:
			return nil, errf(ln, "indented line outside of a section")
		}
	}
	for len(f.DocBody) > 0 && f.DocBody[len(f.DocBody)-1] == "" {
		f.DocBody = f.DocBody[:len(f.DocBody)-1]
	}
	for _, s := range f.Syntax {
		if len(s.Patterns) == 0 {
			return nil, errf(s.Line, "syntax section has no patterns")
		}
	}
	return f, nil
}

// dedent removes one level (a tab or two spaces) of indentation.
func dedent(s string) string {
	if strings.HasPrefix(s, "\t") {
		return s[1:]
	}
	for i := 0; i < 2 && strings.HasPrefix(s, " "); i++ {
		s = s[1:]
	}
	return s
}

func parseSyntaxHeader(args []string) (*Syntax, error) {
	if len(args) == 0 || !syntaxKinds[args[0]] {
		return nil, fmt.Errorf("syntax needs a kind: statement, function, expr, infix or prefix")
	}
	s := &Syntax{Kind: args[0]}
	for _, a := range args[1:] {
		switch a {
		case "left":
		case "right":
			s.Right = true
		default:
			n, err := strconv.Atoi(a)
			if err != nil {
				return nil, fmt.Errorf("unexpected %q in syntax header", a)
			}
			s.Prec = n
		}
	}
	if (s.Kind == "infix" || s.Kind == "prefix") && s.Prec == 0 {
		return nil, fmt.Errorf("%s syntax needs a precedence (a positive number)", s.Kind)
	}
	return s, nil
}

func parseEmitHeader(args []string) (*Emit, error) {
	e := &Emit{}
	if len(args) > 0 && args[0] != "when" {
		e.Section = args[0]
		args = args[1:]
	}
	if len(args) == 0 {
		return e, nil
	}
	if args[0] != "when" {
		return nil, fmt.Errorf("expected 'when' in emit header")
	}
	args = args[1:]
	switch {
	case len(args) == 3 && (args[1] == "is" || args[1] == "has"):
		e.Cond = &Cond{Slot: args[0], Op: args[1], Arg: args[2]}
	case len(args) == 2 && (args[1] == "simple" || args[1] == "empty"):
		e.Cond = &Cond{Slot: args[0], Op: args[1]}
	case len(args) == 2:
		e.Cond = &Cond{Slot: args[0], Op: "type", Arg: args[1]}
	default:
		return nil, fmt.Errorf("bad condition; use: when <slot> <type> | <slot> is <kind> | <slot> has <section> | <slot> simple | <slot> empty")
	}
	return e, nil
}

// ParsePattern parses one pattern line.
func ParsePattern(s string) (*Pattern, error) {
	toks, err := patternTokens(s)
	if err != nil {
		return nil, err
	}
	p := &Pattern{}
	if n := len(toks); n >= 2 && toks[n-2] == "->" {
		p.Result = toks[n-1]
		toks = toks[:n-2]
	}
	elems, rest, err := parseElems(toks, false)
	if err != nil {
		return nil, err
	}
	if len(rest) > 0 {
		return nil, fmt.Errorf("unbalanced ']' in pattern")
	}
	if len(elems) == 0 {
		return nil, fmt.Errorf("empty pattern")
	}
	p.Elems = elems
	return p, nil
}

func patternTokens(s string) ([]string, error) {
	var out []string
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == ' ' || c == '\t':
			i++
		case c == '[' || c == ']':
			out = append(out, string(c))
			i++
		case c == '{':
			j := strings.IndexByte(s[i:], '}')
			if j < 0 {
				return nil, fmt.Errorf("unterminated slot in pattern")
			}
			out = append(out, s[i:i+j+1])
			i += j + 1
		case c == '\\' && i+1 < len(s):
			j := i + 1
			for j < len(s) && s[j] != ' ' && s[j] != '\t' {
				j++
			}
			out = append(out, "\x00"+s[i+1:j]) // escaped literal
			i = j
		default:
			j := i
			for j < len(s) && s[j] != ' ' && s[j] != '\t' && s[j] != '[' && s[j] != ']' && s[j] != '{' {
				j++
			}
			out = append(out, s[i:j])
			i = j
		}
	}
	return out, nil
}

func parseElems(toks []string, inGroup bool) ([]Elem, []string, error) {
	var elems []Elem
	for len(toks) > 0 {
		t := toks[0]
		toks = toks[1:]
		switch {
		case t == "[":
			group, rest, err := parseElems(toks, true)
			if err != nil {
				return nil, nil, err
			}
			if len(group) == 0 || group[0].Lit == "" {
				return nil, nil, fmt.Errorf("an optional group must start with a literal")
			}
			elems = append(elems, Elem{Optional: group})
			toks = rest
		case t == "]":
			if !inGroup {
				return nil, nil, fmt.Errorf("unbalanced ']' in pattern")
			}
			return elems, toks, nil
		case strings.HasPrefix(t, "{"):
			sl, err := parseSlot(t)
			if err != nil {
				return nil, nil, err
			}
			elems = append(elems, Elem{Slot: sl})
		default:
			elems = append(elems, Elem{Lit: strings.TrimPrefix(t, "\x00")})
		}
	}
	if inGroup {
		return nil, nil, fmt.Errorf("unclosed '[' in pattern")
	}
	return elems, nil, nil
}

func parseSlot(t string) (*Slot, error) {
	body := strings.TrimSuffix(strings.TrimPrefix(t, "{"), "}")
	name, spec, ok := strings.Cut(body, ":")
	if !ok {
		return nil, fmt.Errorf("slot %s needs a kind, e.g. {name: expr}", t)
	}
	fields := strings.Fields(spec)
	if len(fields) == 0 || !slotKinds[fields[0]] {
		return nil, fmt.Errorf("slot %s: unknown kind (expr, block, newvar, var, ident, func, newfunc, args, params)", t)
	}
	s := &Slot{Name: strings.TrimSpace(name), Kind: fields[0], Arg: strings.Join(fields[1:], " ")}
	if s.Kind == "newvar" && s.Arg != "" {
		of, ok := strings.CutPrefix(s.Arg, "of ")
		if !ok {
			return nil, fmt.Errorf("slot %s: newvar takes 'of <slot>'", t)
		}
		s.Arg = strings.TrimSpace(of)
	}
	return s, nil
}

func checkPattern(s *Syntax, p *Pattern) error {
	isExpr := func(e Elem) bool { return e.Slot != nil && e.Slot.Kind == "expr" }
	switch s.Kind {
	case "infix":
		if len(p.Elems) != 3 || !isExpr(p.Elems[0]) || p.Elems[1].Lit == "" || !isExpr(p.Elems[2]) {
			return fmt.Errorf("infix pattern must look like {a: expr} OP {b: expr}")
		}
	case "prefix":
		if len(p.Elems) != 2 || p.Elems[0].Lit == "" || !isExpr(p.Elems[1]) {
			return fmt.Errorf("prefix pattern must look like OP {a: expr}")
		}
	}
	if _, err := blockClosers(p.Elems); err != nil {
		return err
	}
	return nil
}

// follow returns the literals that can come right after es[i], given what
// can follow the whole list.
func follow(es []Elem, i int, after []string) []string {
	var out []string
	for j := i + 1; j < len(es); j++ {
		switch {
		case es[j].Lit != "":
			return append(out, es[j].Lit)
		case es[j].Optional != nil:
			out = append(out, es[j].Optional[0].Lit)
		default:
			return out
		}
	}
	return append(out, after...)
}

func blockClosers(es []Elem) ([]string, error) {
	var out []string
	var walk func(es []Elem, after []string) error
	walk = func(es []Elem, after []string) error {
		for i, e := range es {
			if e.Optional != nil {
				if err := walk(e.Optional, follow(es, i, after)); err != nil {
					return err
				}
			}
			if e.Slot != nil && e.Slot.Kind == "block" {
				f := follow(es, i, after)
				if len(f) == 0 {
					return fmt.Errorf("block slot {%s} must be followed by a literal (like 'end')", e.Slot.Name)
				}
				out = append(out, f...)
			}
		}
		return nil
	}
	err := walk(es, nil)
	return out, err
}

// Literals returns every literal in the pattern, including optional groups.
func (p *Pattern) Literals() []string {
	var out []string
	var walk func([]Elem)
	walk = func(es []Elem) {
		for _, e := range es {
			if e.Lit != "" {
				out = append(out, e.Lit)
			}
			walk(e.Optional)
		}
	}
	walk(p.Elems)
	return out
}

// Closers returns literals that can directly follow a block slot.
func (p *Pattern) Closers() []string {
	out, _ := blockClosers(p.Elems)
	return out
}

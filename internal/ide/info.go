package ide

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/DominicThemiya/Packenger/internal/lang"
	"github.com/DominicThemiya/Packenger/internal/pkgfile"
)

// Info describes the word under the cursor, and where it is defined.
type Info struct {
	Word  string    `json:"word"`
	Title string    `json:"title,omitempty"` // a signature, a type, a pattern
	Doc   string    `json:"doc,omitempty"`
	Def   *Location `json:"def,omitempty"`
}

// A Location is a line in a file of the workspace (File, absolute) or in a
// package from the registry (Package).
type Location struct {
	File    string `json:"file,omitempty"`
	Package string `json:"package,omitempty"`
	Line    int    `json:"line"` // 1-based
}

// wordAt finds the word around column col (a byte offset) of a line.
func wordAt(text string, col int, extra string) (before, word string) {
	col = min(max(col, 0), len(text))
	before, w := trailingWord(text[:col], extra)
	rest := text[col:]
	i := 0
	for i < len(rest) {
		c := rest[i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(extra, c) >= 0 {
			i++
			continue
		}
		break
	}
	return before, w + rest[:i]
}

// packageInfo describes a package: its summary and the start of its doc.
func (e *Engine) packageInfo(ctx context.Context, src Source, name string) *Info {
	text, origin, err := e.FetchPackage(ctx, src, name)
	if err != nil {
		return &Info{Word: name, Title: name, Doc: err.Error()}
	}
	f, err := pkgfile.Parse(name, origin, text)
	if err != nil {
		return &Info{Word: name, Title: name, Doc: err.Error(), Def: &Location{Package: name, Line: 1}}
	}
	body := f.DocBody
	if len(body) > 14 {
		body = append(body[:14:14], "…")
	}
	doc := f.Doc
	if len(body) > 0 {
		doc += "\n\n" + strings.Join(body, "\n")
	}
	if len(f.Truck) > 0 {
		doc += "\n\ntruck: " + strings.Join(f.Truck, " ")
	}
	return &Info{Word: name, Title: "package " + name, Doc: doc, Def: &Location{Package: name, Line: 1}}
}

// Hover describes the word at column col (bytes) of line (0-based).
func (e *Engine) Hover(ctx context.Context, src Source, line, col int) (*Info, error) {
	lines := strings.Split(src.Text, "\n")
	if line < 0 || line >= len(lines) {
		return nil, nil
	}
	text := lines[line]
	program := IsProgram(src.Text)
	if f := strings.Fields(text); len(f) > 1 && (program && f[0] == "use" || !program && f[0] == "needs" && text[0] == 'n') {
		_, w := wordAt(text, col, "/-")
		if w != "" && w != f[0] {
			return e.packageInfo(ctx, src, w), nil
		}
		return nil, nil
	}
	if !program {
		if section, header := sectionAt(lines, line); section != "code" || header == line {
			return nil, nil
		}
	}
	before, w := wordAt(text, col, "")
	if w == "" {
		return nil, nil
	}
	v, err := e.Analyze(ctx, src, -1)
	if err != nil || v.A == nil {
		return nil, err
	}
	s := v.scope(line)
	info := &Info{Word: w}
	file := func(p lang.Pos) *Location {
		if p.File == s.file {
			return &Location{File: src.Path, Line: p.Line - v.Offset}
		}
		return &Location{Package: strings.TrimSuffix(p.File, ".pkg"), Line: p.Line}
	}
	// a field: x.name
	if strings.HasSuffix(strings.TrimRight(before, " "), ".") {
		toks, err := lang.Lex(strings.TrimRight(before, " "), "x", v.A.Lang.Puncts, false)
		if err == nil {
			for len(toks) > 0 && (toks[len(toks)-1].Kind == lang.TEOF || toks[len(toks)-1].Kind == lang.TNewline) {
				toks = toks[:len(toks)-1]
			}
			typ := s.exprType(toks[:len(toks)-1])
			if f := s.field(typ, w); f != nil {
				info.Title = strings.TrimPrefix(typ, "ptr ") + "." + w + ": " + f.Type
				if st := s.structOf(typ); st != nil {
					info.Def = structDef(s, st.Name, file)
				}
				return info, nil
			}
		}
	}
	for i := len(s.locals) - 1; i >= 0; i-- {
		if l := s.locals[i]; l.Name == w {
			kind := "variable"
			if s.params[l] {
				kind = "parameter"
			}
			info.Title = w + ": " + l.Type
			info.Doc = kind
			if p, ok := s.decls[l]; ok {
				info.Def = file(p)
			}
			return info, nil
		}
	}
	if f := s.findFunc(w); f != nil {
		info.Title = "fn " + signature(f)
		doc, where := s.funcDoc(f, lines, v.Offset)
		if where != "" {
			doc = strings.TrimSpace(doc + "\n\nfrom " + where)
		}
		info.Doc = doc
		info.Def = file(f.Pos)
		return info, nil
	}
	if g := s.lookupVar(w); g != nil {
		info.Title = "global " + w + ": " + g.Type
		if p, ok := s.decls[g]; ok {
			info.Def = file(p)
		}
		return info, nil
	}
	if c := s.consts()[w]; c != nil {
		info.Title = "const " + w
		if val := c.Slots["value"]; val != nil {
			info.Title += ": " + val.Type
		}
		info.Def = file(c.Pos)
		return info, nil
	}
	if s.prog != nil {
		if st := s.prog.Types.Structs[w]; st != nil {
			var fs []string
			for _, f := range st.Fields {
				fs = append(fs, "  "+f.Name+": "+f.Type)
			}
			info.Title = "struct " + w
			info.Doc = strings.Join(fs, "\n")
			info.Def = structDef(s, w, file)
			return info, nil
		}
	}
	// a keyword: the patterns that use it, and their packages
	var pats, docs []string
	var def *Location
	for _, name := range sortedNames(v.A.Front) {
		f := v.A.Front[name]
		for _, sy := range f.Syntax {
			for _, p := range sy.Patterns {
				for _, lit := range p.Literals() {
					if lit == w {
						pats = append(pats, patternText(p.Elems))
						if len(docs) == 0 || docs[len(docs)-1] != name+": "+f.Doc {
							docs = append(docs, name+": "+f.Doc)
						}
						if def == nil {
							def = &Location{Package: name, Line: p.Line}
						}
						break
					}
				}
			}
		}
	}
	if len(pats) > 0 {
		info.Title = strings.Join(dedupe(pats), "\n")
		info.Doc = strings.Join(docs, "\n")
		info.Def = def
		return info, nil
	}
	return nil, nil
}

func structDef(s *scope, name string, file func(lang.Pos) *Location) *Location {
	if s.prog == nil {
		return nil
	}
	for _, n := range s.prog.Structs {
		for _, slot := range n.Order {
			if c := n.Slots[slot]; c.Kind == "newtype" && c.Ident == name {
				return file(n.Pos)
			}
		}
	}
	return nil
}

func dedupe(xs []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// Symbol is an entry of a file's outline.
type Symbol struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"` // fn, struct, global, const, section
	Detail string `json:"detail,omitempty"`
	Line   int    `json:"line"` // 1-based
}

// Outline lists what a file defines: its functions, structs, globals and
// constants, or a package file's sections.
func (e *Engine) Outline(ctx context.Context, src Source) []Symbol {
	var out []Symbol
	lines := strings.Split(src.Text, "\n")
	offset := 0
	if !IsProgram(src.Text) {
		for i, l := range lines {
			if l != "" && l[0] != ' ' && l[0] != '\t' {
				f := strings.Fields(l)
				out = append(out, Symbol{Name: f[0], Kind: "section", Detail: strings.Join(f[1:], " "), Line: i + 1})
			}
		}
		_, offset, _ = codeAsProgram(src.Text)
	}
	v, err := e.Analyze(ctx, src, -1)
	if err != nil || v.A == nil || v.A.Program == nil {
		return out
	}
	file := filepath.Base(src.Path)
	prog := v.A.Program
	for _, n := range prog.Top {
		if n.Pos.File != file {
			continue
		}
		line := n.Pos.Line - offset
		switch {
		case n.Frame != nil:
			out = append(out, Symbol{Name: n.Frame.Name, Kind: "fn", Detail: signature(n), Line: line})
		default:
			for _, name := range n.Order {
				c := n.Slots[name]
				switch {
				case c.Kind == "newtype":
					out = append(out, Symbol{Name: c.Ident, Kind: "struct", Line: line})
				case c.Kind == "newconst":
					out = append(out, Symbol{Name: c.Ident, Kind: "const", Line: line})
				case c.Kind == "var" && c.Var != nil && c.Var.Global && n.Rule != nil && n.Rule.Syn.Top:
					out = append(out, Symbol{Name: c.Var.Name, Kind: "global", Detail: c.Var.Type, Line: line})
				}
			}
		}
	}
	return out
}

// Grammar describes the block structure and words of the grammar a file
// loads, for highlighting and indentation in the editor.
type Grammar struct {
	Keywords []string `json:"keywords"`
	Symbols  []string `json:"symbols"`
	Openers  []string `json:"openers"`
	Closers  []string `json:"closers"`
	Reopen   []string `json:"reopen"`
	Args     []string `json:"args"`
	Types    []string `json:"types"`
	Uses     []string `json:"uses"`
}

func keys(m map[string]bool) []string { return sortedNames(m) }

// Grammar returns the grammar of a program, or of a package file's code.
func (e *Engine) Grammar(ctx context.Context, src Source) (*Grammar, error) {
	text := src.Text
	if !IsProgram(text) {
		var ok bool
		if text, _, ok = codeAsProgram(text); !ok {
			needs := needsOf(strings.Split(src.Text, "\n"))
			if len(needs) == 0 {
				return &Grammar{}, nil
			}
			text = "use " + strings.Join(needs, "\nuse ") + "\n"
		}
	}
	// only the use lines matter
	var head []string
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == "" || isUseLine(l) || strings.HasPrefix(strings.TrimSpace(l), "//") {
			head = append(head, l)
			continue
		}
		break
	}
	a, err := e.analyzeText(ctx, src, strings.Join(head, "\n")+"\n")
	if err != nil {
		return nil, err
	}
	g := blockInfo(a.Front)
	gr := &Grammar{Keywords: a.Lang.Keywords(), Symbols: a.Lang.Puncts, Openers: keys(g.openers), Closers: keys(g.closers), Reopen: keys(g.reopen), Args: keys(g.args), Uses: a.Uses}
	for _, f := range a.Front {
		for _, t := range f.Types {
			gr.Types = append(gr.Types, t.Name)
		}
	}
	gr.Types = append(gr.Types, "ptr")
	return gr, nil
}

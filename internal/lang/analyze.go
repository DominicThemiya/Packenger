package lang

import (
	"context"
	"sort"

	"github.com/DominicThemiya/Packenger/internal/pkgfile"
)

// An Analysis is what editor tooling needs about a source file: the grammar
// it loads and, if it parses, its syntax tree. See internal/ide.
type Analysis struct {
	Uses    []string
	Front   map[string]*pkgfile.File // every front-end package loaded, by name
	Lang    *Lang
	Program *Program // nil if the file doesn't lex or parse
	Err     error    // why it doesn't
}

// Analyze loads the grammar src uses and parses it, without generating
// code. It fails only if the grammar can't be loaded; a lexing or parsing
// error is left in the Analysis, next to the grammar.
func (c *Compiler) Analyze(ctx context.Context, file, src string) (*Analysis, error) {
	body, uses, front, L, err := c.grammar(ctx, file, src)
	if err != nil {
		return nil, err
	}
	a := &Analysis{Uses: uses, Front: front, Lang: L}
	toks, err := Lex(body, file, L.Puncts, false)
	if err != nil {
		a.Err = err
		return a, nil
	}
	units, _, err := c.libraryUnits(front, L)
	if err != nil {
		a.Err = err
		return a, nil
	}
	a.Program, a.Err = Parse(L, append(units, Unit{Name: file, Toks: toks}))
	return a, nil
}

// Keywords lists the grammar's keywords, sorted.
func (L *Lang) Keywords() []string { return sortedKeys(L.keywords) }

// IsCloser reports whether a literal can end a block (end, else, next).
func (L *Lang) IsCloser(lit string) bool { return L.closers[lit] }

// Span returns the positions of the first and last tokens n was parsed
// from. ok is false for nodes made by the compiler.
func (n *Node) Span() (first, last Pos, ok bool) {
	if n.toks == nil || n.start >= n.end || n.end > len(n.toks) {
		return Pos{}, Pos{}, false
	}
	return n.toks[n.start].Pos, n.toks[n.end-1].Pos, true
}

// StructNames lists the program's structs, sorted.
func (ts *Types) StructNames() []string {
	out := make([]string, 0, len(ts.Structs))
	for n := range ts.Structs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

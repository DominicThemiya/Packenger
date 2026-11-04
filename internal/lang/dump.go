package lang

import (
	"fmt"
	"strings"

	"github.com/DominicThemiya/Packenger/internal/pkgfile"
)

var tokKindNames = map[TokKind]string{
	TEOF: "eof", TNewline: "newline", TIdent: "ident", TInt: "int", TStr: "str", TPunct: "punct", THole: "hole",
}

// DumpTokens renders a token stream in the stable format documented in
// docs/dumps.md: one token per line.
func DumpTokens(toks []Token) string {
	var b strings.Builder
	for _, t := range toks {
		fmt.Fprintf(&b, "%d:%d %s", t.Pos.Line, t.Pos.Col, tokKindNames[t.Kind])
		switch t.Kind {
		case TIdent, TPunct, TInt:
			b.WriteString(" " + t.Text)
		case TStr:
			b.WriteString(" " + pkgfile.Quote(t.Text))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// DumpAST renders a parsed program in the stable format documented in
// docs/dumps.md.
func DumpAST(prog *Program) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("program frame locals=%d size=%d\n", prog.Frame.Slots, prog.Frame.Size()))
	var walk func(n *Node, depth int, label string)
	walk = func(n *Node, depth int, label string) {
		b.WriteString(strings.Repeat("  ", depth) + label + nodeLine(n) + "\n")
		for _, name := range n.Order {
			walk(n.Slots[name], depth+1, name+": ")
		}
		for _, c := range n.Items {
			walk(c, depth+1, "- ")
		}
	}
	for _, n := range prog.Top {
		walk(n, 1, "- ")
	}
	return b.String()
}

func nodeLine(n *Node) string {
	s := fmt.Sprintf("%s #%d @%s", n.Kind, n.ID, n.Pos)
	if n.Type != "" {
		s += " : " + n.Type
	}
	switch n.Kind {
	case "int":
		s += fmt.Sprintf(" = %d", n.Int)
	case "str":
		s += " = " + pkgfile.Quote(n.Str)
	case "var":
		where := "local"
		if n.Var.Param {
			where = "param"
		}
		s += fmt.Sprintf(" %s %s %d", n.Var.Name, where, n.Var.Index)
	case "ident", "func", "newfunc":
		s += " " + n.Ident
	}
	if len(n.Expanded) > 0 {
		s += " expanded=" + strings.Join(n.Expanded, ",")
	}
	if f := n.Frame; f != nil {
		s += fmt.Sprintf(" frame name=%s params=%d locals=%d size=%d", f.Name, f.Params, f.Slots, f.Size())
	}
	return s
}

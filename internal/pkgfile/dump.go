package pkgfile

import (
	"fmt"
	"strings"
)

// String renders a pattern in package-file syntax, normalized: single
// spaces, optional groups as "[ ... ]".
func (p *Pattern) String() string {
	var walk func([]Elem) string
	walk = func(es []Elem) string {
		var ps []string
		for _, e := range es {
			switch {
			case e.Lit != "":
				ps = append(ps, e.Lit)
			case e.Slot != nil:
				s := e.Slot.Name + ": " + e.Slot.Kind
				switch {
				case e.Slot.Kind == "newvar" && e.Slot.Arg != "":
					s += " of " + e.Slot.Arg
				case e.Slot.Arg != "":
					s += " " + e.Slot.Arg
				}
				ps = append(ps, "{"+s+"}")
			case e.Optional != nil:
				ps = append(ps, "[ "+walk(e.Optional)+" ]")
			}
		}
		return strings.Join(ps, " ")
	}
	out := walk(p.Elems)
	if p.Result != "" {
		out += " -> " + p.Result
	}
	return out
}

// Quote renders s for dumps: printable ASCII other than " and \ as is,
// everything else as \xHH, inside double quotes. It is defined byte by byte
// so that any implementation can reproduce it exactly.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c <= 0x7e && c != '"' && c != '\\' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "\\x%02x", c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Dump renders the parsed structure of a package file in the stable format
// documented in docs/dumps.md.
func Dump(f *File) string {
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	body := func(lines []string) {
		for _, l := range lines {
			if l == "" {
				line("  |")
			} else {
				line("  | %s", l)
			}
		}
	}
	line("package %s", f.Name)
	line("doc %s", Quote(f.Doc))
	body(f.DocBody)
	if len(f.Needs) > 0 {
		line("needs %s", strings.Join(f.Needs, " "))
	}
	if f.IsTruck {
		line("truck %s", strings.Join(f.Truck, " "))
	}
	for _, s := range f.Syntax {
		assoc := "left"
		if s.Right {
			assoc = "right"
		}
		line("syntax %s prec=%d assoc=%s", s.Kind, s.Prec, assoc)
		for _, p := range s.Patterns {
			line("  pattern %s", p.String())
		}
	}
	if f.Expand != nil {
		line("expand")
		body(f.Expand)
	}
	for _, e := range f.Emits {
		head := "emit section=" + Quote(e.Section)
		if c := e.Cond; c != nil {
			head += fmt.Sprintf(" when slot=%s op=%s arg=%s", c.Slot, c.Op, Quote(c.Arg))
		}
		line("%s", head)
		body(e.Lines)
	}
	if len(f.Data) > 0 {
		line("data")
		body(f.Data)
	}
	if len(f.Support) > 0 {
		line("support")
		body(f.Support)
	}
	return b.String()
}

package lang

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/DominicThemiya/Packenger/internal/pkgfile"
)

// Gen renders an AST through a backend truck (e.g. bintables). Every node
// kind, including integer literals, variables and the program skeleton, is
// rendered by the backend package of the same name.
type Gen struct {
	Target   string
	Back     map[string]*pkgfile.File // kind -> backend package
	Types    *Types
	funcs    map[string]*Node
	data     []string
	support  []string
	used     map[string]bool
	dataDone map[int]bool
	Chosen   map[VariantUse]int // emit section -> uses (Count left zero)
}

func NewGen(target string, back map[string]*pkgfile.File, prog *Program) *Gen {
	g := &Gen{Target: target, Back: back, Types: prog.Types, funcs: map[string]*Node{}, used: map[string]bool{}, dataDone: map[int]bool{}, Chosen: map[VariantUse]int{}}
	for _, f := range prog.Funcs {
		for _, name := range f.Order {
			if c := f.Slots[name]; c.Kind == "newfunc" {
				g.funcs[c.Ident] = f
			}
		}
	}
	return g
}

// Kinds lists the backend packages a program needs.
func Kinds(prog *Program) map[string]bool {
	kinds := map[string]bool{"program": true}
	add := func(n *Node) {
		if !notCode[n.Kind] {
			kinds[n.Kind] = true
		}
	}
	Walk(prog.Main, add)
	for _, f := range prog.Funcs {
		Walk(f, add)
	}
	return kinds
}

// notCode are node kinds that are only data for their parent's template.
var notCode = map[string]bool{"block": true, "ident": true, "func": true, "newfunc": true, "args": true, "params": true,
	"type": true, "field": true, "fields": true, "fielddecl": true, "newtype": true}

// Includes lists the packages a backend package pulls in with "@name".
func Includes(f *pkgfile.File) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range f.Emits {
		for _, l := range e.Lines {
			if strings.HasPrefix(l, "@") {
				if n := strings.Fields(l[1:])[0]; !seen[n] {
					seen[n] = true
					out = append(out, n)
				}
			}
		}
	}
	return out
}

func (g *Gen) Program(prog *Program) (string, error) {
	main, err := g.gen(prog.Main, "", nil)
	if err != nil {
		return "", err
	}
	var funcs []string
	for _, f := range prog.Funcs {
		code, err := g.gen(f, "", nil)
		if err != nil {
			return "", err
		}
		funcs = append(funcs, code...)
	}
	root := &Node{Kind: "program", Slots: map[string]*Node{}, Frame: prog.Frame}
	pkg := g.Back["program"]
	em, err := g.choose(pkg, "", root)
	if err != nil {
		return "", err
	}
	params := map[string][]string{"main": main, "functions": funcs}
	g.use(pkg)
	// data and support are complete only after everything else is rendered
	params["data"], params["support"] = g.data, g.support
	lines, err := g.render(pkg, em.Lines, root, params, nil)
	if err != nil {
		return "", err
	}
	return format(lines), nil
}

func format(lines []string) string {
	var sb strings.Builder
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		first := strings.Fields(l)[0]
		switch {
		case strings.HasSuffix(first, ":") && !strings.Contains(l, " "),
			first == "section", first == "global", first == "bits", first == "default", first == "extern":
		default:
			sb.WriteString("    ")
		}
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (g *Gen) use(pkg *pkgfile.File) {
	if g.used[pkg.Name] {
		return
	}
	g.used[pkg.Name] = true
	g.support = append(g.support, pkg.Support...)
}

func (g *Gen) backend(kind string, n *Node) (*pkgfile.File, error) {
	pkg := g.Back[kind]
	if pkg == nil {
		return nil, fmt.Errorf("%s: target %s has no package for %q (expected %s/%s.pkg)", n.Pos, g.Target, kind, g.Target, kind)
	}
	return pkg, nil
}

func (g *Gen) gen(n *Node, section string, params map[string][]string) ([]string, error) {
	switch n.Kind {
	case "block":
		var out []string
		for _, c := range n.Items {
			code, err := g.gen(c, "", nil)
			if err != nil {
				return nil, err
			}
			out = append(out, code...)
		}
		return out, nil
	case "ident", "func", "newfunc", "args", "params", "type", "field", "fields", "fielddecl", "newtype":
		return nil, fmt.Errorf("%s: a %s slot is not code; use an attribute like {name.label}", n.Pos, n.Kind)
	}
	pkg, err := g.backend(n.Kind, n)
	if err != nil {
		return nil, err
	}
	em, err := g.choose(pkg, section, n)
	if err != nil {
		return nil, err
	}
	g.use(pkg)
	out, err := g.render(pkg, em.Lines, n, params, nil)
	if err != nil {
		return nil, err
	}
	if len(pkg.Data) > 0 && !g.dataDone[n.ID] {
		g.dataDone[n.ID] = true
		d, err := g.render(pkg, pkg.Data, n, nil, nil)
		if err != nil {
			return nil, err
		}
		g.data = append(g.data, d...)
	}
	return out, nil
}

func (g *Gen) choose(pkg *pkgfile.File, section string, n *Node) (*pkgfile.Emit, error) {
	for _, e := range pkg.Emits {
		if e.Section != section {
			continue
		}
		ok, err := g.cond(e.Cond, n)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", pkg.Origin, e.Line, err)
		}
		if ok {
			v := VariantUse{Package: pkg.Name, Section: e.Section}
			if c := e.Cond; c != nil {
				switch c.Op {
				case "type":
					v.When = c.Slot + " " + c.Arg
				case "simple", "empty":
					v.When = c.Slot + " " + c.Op
				default:
					v.When = c.Slot + " " + c.Op + " " + c.Arg
				}
			}
			g.Chosen[v]++
			return e, nil
		}
	}
	name := "emit"
	if section != "" {
		name += " " + section
	}
	return nil, fmt.Errorf("%s: %s has no '%s' section that applies here", n.Pos, pkg.Name, name)
}

func (g *Gen) cond(c *pkgfile.Cond, n *Node) (bool, error) {
	if c == nil {
		return true, nil
	}
	s := pathNode(n, c.Slot)
	if c.Op == "missing" {
		return s == nil, nil
	}
	if s == nil {
		return false, nil
	}
	switch c.Op {
	case "type":
		return s.Type == c.Arg, nil
	case "is":
		return s.Kind == c.Arg, nil
	case "has":
		pkg := g.Back[s.Kind]
		if pkg == nil {
			return false, nil
		}
		for _, e := range pkg.Emits {
			if e.Section == c.Arg {
				return true, nil
			}
		}
		return false, nil
	case "simple":
		return simple(s), nil
	case "empty":
		return len(s.Items) == 0, nil
	case "size":
		size, err := g.Types.Size(s.Type)
		return err == nil && strconv.Itoa(size) == c.Arg, nil
	}
	return false, fmt.Errorf("unknown condition %q", c.Op)
}

// simple nodes can be used directly as an instruction operand.
func simple(n *Node) bool {
	switch n.Kind {
	case "int":
		return n.Int >= -1<<31 && n.Int < 1<<31
	case "var":
		return true
	}
	return false
}

func pathNode(n *Node, path string) *Node {
	for _, seg := range strings.Split(path, ".") {
		if seg == "self" {
			continue
		}
		c, ok := n.Slots[seg]
		if !ok {
			return nil
		}
		n = c
	}
	return n
}

var holeRe = regexp.MustCompile(`\{([^{}]+)\}`)

func (g *Gen) render(pkg *pkgfile.File, lines []string, n *Node, params map[string][]string, it *Node) ([]string, error) {
	var out []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		errf := func(format string, a ...any) error {
			return fmt.Errorf("%s: %s (in %q)", pkg.Origin, fmt.Sprintf(format, a...), line)
		}
		switch {
		case strings.HasPrefix(line, "{each "):
			f := strings.Fields(strings.Trim(line, "{}"))
			if len(f) < 2 || len(f) > 3 || (len(f) == 3 && f[2] != "reverse") {
				return nil, errf("use {each <slot>} or {each <slot> reverse}")
			}
			depth, j := 1, i+1
			for ; j < len(lines); j++ {
				if strings.HasPrefix(lines[j], "{each ") {
					depth++
				} else if lines[j] == "{end}" {
					if depth--; depth == 0 {
						break
					}
				}
			}
			if j >= len(lines) {
				return nil, errf("{each} without {end}")
			}
			list := pathNode(n, f[1])
			if list == nil {
				return nil, errf("unknown slot %s", f[1])
			}
			items := list.Items
			for k := range items {
				item := items[k]
				if len(f) == 3 {
					item = items[len(items)-1-k]
				}
				code, err := g.render(pkg, lines[i+1:j], n, params, item)
				if err != nil {
					return nil, err
				}
				out = append(out, code...)
			}
			i = j
		case strings.HasPrefix(line, "@"):
			f := strings.Fields(line[1:])
			inc := g.Back[f[0]]
			if inc == nil {
				return nil, errf("included package %s/%s was not loaded", g.Target, f[0])
			}
			section := ""
			if len(f) > 1 {
				section = f[1]
			}
			em, err := g.choose(inc, section, n)
			if err != nil {
				return nil, err
			}
			g.use(inc)
			code, err := g.render(inc, em.Lines, n, params, it)
			if err != nil {
				return nil, err
			}
			out = append(out, code...)
		default:
			if m := holeRe.FindStringSubmatch(line); m != nil && m[0] == line {
				val, code, isCode, err := g.value(m[1], n, params, it)
				if err != nil {
					return nil, errf("%v", err)
				}
				if isCode {
					out = append(out, code...)
				} else {
					out = append(out, val)
				}
				continue
			}
			var ferr error
			res := holeRe.ReplaceAllStringFunc(line, func(h string) string {
				val, _, isCode, err := g.value(h[1:len(h)-1], n, params, it)
				if err != nil && ferr == nil {
					ferr = err
				}
				if isCode && ferr == nil {
					ferr = fmt.Errorf("{%s} is code; put it on a line of its own", h[1:len(h)-1])
				}
				return val
			})
			if ferr != nil {
				return nil, errf("%v", ferr)
			}
			out = append(out, res)
		}
	}
	return out, nil
}

// value resolves a placeholder. It returns either a single value or, for a
// node without an attribute, that node's generated code.
func (g *Gen) value(expr string, n *Node, params map[string][]string, it *Node) (string, []string, bool, error) {
	expr = strings.TrimSpace(expr)
	path, call, hasCall := strings.Cut(expr, "!")
	switch {
	case strings.HasPrefix(path, "L."):
		return fmt.Sprintf("_L%d_%s", n.ID, path[2:]), nil, false, nil
	case strings.HasPrefix(path, "D."):
		return fmt.Sprintf("_D%d_%s", n.ID, path[2:]), nil, false, nil
	}
	if v, ok := params[path]; ok {
		if len(v) == 1 && !hasCall {
			return v[0], nil, false, nil
		}
		return "", v, true, nil
	}
	segs := strings.Split(path, ".")
	cur := n
	switch {
	case segs[0] == "it" && it != nil:
		cur, segs = it, segs[1:]
	case segs[0] == "self":
		segs = segs[1:]
	}
	for len(segs) > 0 {
		c, ok := cur.Slots[segs[0]]
		if !ok {
			if cur.Rule != nil && hasSlot(cur.Rule.Pat.Elems, segs[0]) {
				return "", nil, true, nil // optional slot not present: no code
			}
			if len(segs) > 1 {
				return "", nil, false, fmt.Errorf("unknown slot %q", segs[0])
			}
			v, err := g.attr(cur, segs[0])
			return v, nil, false, err
		}
		cur, segs = c, segs[1:]
	}
	section := ""
	var args map[string][]string
	if hasCall {
		f := strings.Fields(call)
		if len(f) == 0 {
			return "", nil, false, fmt.Errorf("{slot!section} needs a section name")
		}
		section = f[0]
		args = map[string][]string{}
		for k, a := range f[1:] {
			v, _, _, err := g.value(a, n, params, it)
			if err != nil {
				return "", nil, false, err
			}
			args[[]string{"target", "arg2", "arg3"}[min(k, 2)]] = []string{v}
		}
	}
	code, err := g.gen(cur, section, args)
	return "", code, true, err
}

func (g *Gen) attr(n *Node, a string) (string, error) {
	switch a {
	case "addr":
		if n.Var == nil {
			break
		}
		if n.Var.Param {
			return fmt.Sprintf("qword [rbp+%d]", 16+8*n.Var.Index), nil
		}
		return fmt.Sprintf("qword [rbp-%d]", 8*(n.Var.Index+1)), nil
	case "label":
		switch n.Kind {
		case "func":
			if _, ok := g.funcs[n.Ident]; !ok {
				return "", fmt.Errorf("%s: unknown function %s", n.Pos, n.Ident)
			}
			return "pk_" + n.Ident, nil
		case "newfunc", "ident":
			return "pk_" + n.Ident, nil
		}
	case "name":
		if n.Var != nil {
			return n.Var.Name, nil
		}
		if n.Ident != "" {
			return n.Ident, nil
		}
	case "value":
		if n.Kind == "int" {
			return strconv.FormatInt(n.Int, 10), nil
		}
	case "operand":
		if simple(n) {
			if n.Kind == "int" {
				return strconv.FormatInt(n.Int, 10), nil
			}
			return g.attr(n, "addr")
		}
	case "len":
		if n.Kind == "str" {
			return strconv.Itoa(len(n.Str)), nil
		}
	case "bytes":
		if n.Kind == "str" {
			if n.Str == "" {
				return "0", nil
			}
			b := make([]string, len(n.Str))
			for i := 0; i < len(n.Str); i++ {
				b[i] = strconv.Itoa(int(n.Str[i]))
			}
			return strings.Join(b, ","), nil
		}
	case "count":
		return strconv.Itoa(len(n.Items)), nil
	case "stack":
		return strconv.Itoa(8 * len(n.Items)), nil
	case "size":
		size, err := g.Types.Size(n.Type)
		if err != nil {
			return "", fmt.Errorf("%s: %v", n.Pos, err)
		}
		return strconv.Itoa(size), nil
	case "alloc":
		// bytes to allocate for a value of this type: a struct's full size
		if st, ok := g.Types.Structs[n.Type]; ok {
			return strconv.Itoa(st.Size), nil
		}
		size, err := g.Types.Size(n.Type)
		if err != nil {
			return "", fmt.Errorf("%s: %v", n.Pos, err)
		}
		return strconv.Itoa(size), nil
	case "elemsize":
		size, err := g.Types.Size(Elem(n.Type))
		if err != nil {
			return "", fmt.Errorf("%s: %v", n.Pos, err)
		}
		return strconv.Itoa(size), nil
	case "offset":
		if n.Field != nil {
			return strconv.Itoa(n.Field.Offset), nil
		}
	case "mem":
		if n.Var != nil {
			a, _ := g.attr(n, "addr")
			return strings.TrimPrefix(a, "qword "), nil
		}
	case "frame":
		if n.Frame != nil {
			return strconv.Itoa(n.Frame.Size()), nil
		}
	case "type":
		return n.Type, nil
	}
	return "", fmt.Errorf("%s has no attribute or slot %q", n.Kind, a)
}

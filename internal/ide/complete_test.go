package ide

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func engine(t *testing.T) *Engine {
	reg, _ := filepath.Abs("../../registry")
	return NewEngine("file://" + reg)
}

// complete completes at the | in text.
func complete(t *testing.T, e *Engine, path, text string) *Completion {
	t.Helper()
	at := strings.Index(text, "|")
	text = text[:at] + text[at+1:]
	line := strings.Count(text[:at], "\n")
	prefix := text[strings.LastIndex(text[:at], "\n")+1 : at]
	c, err := e.Complete(context.Background(), Source{Path: path, Text: text}, line, prefix)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func labels(c *Completion) map[string]Item {
	out := map[string]Item{}
	for _, it := range c.Items {
		if _, ok := out[it.Label]; !ok {
			out[it.Label] = it
		}
	}
	return out
}

func want(t *testing.T, c *Completion, names ...string) {
	t.Helper()
	ls := labels(c)
	for _, n := range names {
		if _, ok := ls[n]; !ok {
			var have []string
			for l := range ls {
				have = append(have, l)
			}
			t.Errorf("missing %q; have %v", n, have)
		}
	}
}

func dontWant(t *testing.T, c *Completion, names ...string) {
	t.Helper()
	ls := labels(c)
	for _, n := range names {
		if _, ok := ls[n]; ok {
			t.Errorf("unexpected %q", n)
		}
	}
}

func TestCompleteProgram(t *testing.T) {
	e := engine(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "a.pkg")
	prog := "use std\nuse rt\n\nstruct Point\n  x: int\n  y: int\nend\n\n// doubles n\nfn twice(n: int) -> int\n  let k = 2\n  |\nend\n"
	c := complete(t, e, p, prog)
	want(t, c, "n", "k", "twice", "if", "while", "return", "end", "str_trim")
	if c.Items[0].Kind != "var" && c.Items[0].Kind != "param" && c.Items[0].Kind != "keyword" {
		t.Errorf("first item %+v", c.Items[0])
	}
	if it := labels(c)["twice"]; it.Detail != "twice(n: int) -> int" || it.Doc != "doubles n" || it.Insert != "twice(${n})" {
		t.Errorf("twice: %+v", it)
	}
	if it := labels(c)["if"]; !strings.Contains(it.Insert, "${cond}") || !strings.Contains(it.Insert, "\n  end") {
		t.Errorf("if snippet: %q", it.Insert)
	}

	// fields after a dot, through new and pointers
	c = complete(t, e, p, "use std\nstruct Point\n  x: int\n  y: int\nend\nlet p = new Point\nprint p.|\n")
	want(t, c, "x", "y")

	// a block being written, without its end: the open block is closed
	c = complete(t, e, p, "use std\nfn f(a: int) -> int\n  let b = a\n  if b > 1\n    |\n")
	want(t, c, "a", "b", "else", "elif", "end")

	// what comes next in the statement
	c = complete(t, e, p, "use std\nfor i = 0 |\n")
	want(t, c, "to")
	dontWant(t, c, "end")
	if !strings.Contains(c.Hint, "for {i} = {from} to {to}") {
		t.Errorf("hint %q", c.Hint)
	}

	// types
	c = complete(t, e, p, "use std\nstruct Point\n  x: int\nend\nlet q: |\n")
	want(t, c, "int", "str", "Point", "ptr")

	// package names in use lines
	c = complete(t, e, p, "use std\nuse rt/s|\n")
	want(t, c, "rt/str", "rt/sort")
	if c.Word != "rt/s" {
		t.Errorf("word %q", c.Word)
	}

	// a call's signature
	c = complete(t, e, p, "use std\nfn add(a: int, b: int) -> int\n  return a + b\nend\nprint add(1, |\n")
	if c.Hint != "add(a: int, ‹b: int›) -> int" {
		t.Errorf("call hint %q", c.Hint)
	}

	// nothing inside strings or comments
	if c = complete(t, e, p, "use std\nprint \"ab|\n"); len(c.Items) != 0 {
		t.Errorf("completion in a string")
	}
}

func TestCompletePackage(t *testing.T) {
	e := engine(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "lib.pkg")
	c := complete(t, e, p, "doc A library.\n|")
	want(t, c, "needs", "code", "syntax")
	c = complete(t, e, p, "doc A library.\nneeds rt/ve|")
	want(t, c, "rt/vec")
	c = complete(t, e, p, "doc A library.\nneeds std rt/vec\ncode\n  fn f(xs: ptr Vec) -> int\n    return vec_|\n  end\n")
	want(t, c, "vec_len", "vec_push", "xs")
	c = complete(t, e, p, "doc A loop.\nsyntax statement\n  repeat {n: |\n")
	want(t, c, "expr", "block", "newvar")
	c = complete(t, e, p, "doc A loop.\nneeds std\nsyntax statement\n  repeat {n: expr int} {body: block} end\nexpand\n  for $i = 1 to {|\n")
	want(t, c, "n", "body", "@pos")
	c = complete(t, e, p, "doc fixes\nneeds resolve rt\nresolve\n  rename prefix 'fs_' in |\nend\n")
	want(t, c, "rt", "resolve")
	os.WriteFile(filepath.Join(dir, "lib.pkg"), nil, 0o644)
}

func TestHoverAndOutline(t *testing.T) {
	e := engine(t)
	p := filepath.Join(t.TempDir(), "a.pkg")
	text := "use std\nuse rt\n\nstruct Point\n  x: int\nend\n\n// doubles n\nfn twice(n: int) -> int\n  return n * 2\nend\nlet p = new Point\nprint twice(p.x)\nprint str_trim(\" a \")\n"
	src := Source{Path: p, Text: text}
	hover := func(line, col int) *Info {
		i, err := e.Hover(context.Background(), src, line, col)
		if err != nil {
			t.Fatal(err)
		}
		if i == nil {
			t.Fatalf("no hover at %d:%d", line, col)
		}
		return i
	}
	if i := hover(12, 7); i.Title != "fn twice(n: int) -> int" || i.Doc != "doubles n" || i.Def == nil || i.Def.Line != 9 || i.Def.File != p {
		t.Errorf("twice: %+v %+v", i, i.Def)
	}
	if i := hover(12, 14); i.Title != "Point.x: int" {
		t.Errorf("p.x: %+v", i)
	}
	if i := hover(13, 8); !strings.Contains(i.Doc, "rt/str") || i.Def.Package != "rt/str" {
		t.Errorf("str_trim: %+v %+v", i, i.Def)
	}
	if i := hover(1, 5); !strings.HasPrefix(i.Title, "package rt") {
		t.Errorf("use rt: %+v", i)
	}
	if i := hover(9, 3); !strings.Contains(i.Title, "return") {
		t.Errorf("return: %+v", i)
	}
	syms := e.Outline(context.Background(), src)
	var names []string
	for _, s := range syms {
		names = append(names, s.Kind+" "+s.Name)
	}
	if strings.Join(names, ",") != "struct Point,fn twice" {
		t.Errorf("outline %v", names)
	}
	g, err := e.Grammar(context.Background(), src)
	if err != nil || len(g.Openers) == 0 || !strings.Contains(strings.Join(g.Closers, " "), "end") {
		t.Errorf("grammar %+v %v", g, err)
	}
	if d := e.Check(context.Background(), Source{Path: p, Text: "use std\nprint nosuch\n"}); len(d) != 1 || d[0].Line != 2 || d[0].Col != 7 {
		t.Errorf("check %+v", d)
	}
}

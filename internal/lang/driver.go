package lang

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DominicThemiya/Packenger/internal/pkgfile"
	"github.com/DominicThemiya/Packenger/internal/registry"
)

type Compiler struct {
	Registry *registry.Registry
	Target   string   // backend truck, e.g. "bintables"
	Report   Reporter // optional
}

type Result struct {
	Asm     string
	Uses    []string
	Front   map[string]*pkgfile.File // every front-end package loaded, by name
	Back    map[string]*pkgfile.File // backend packages, by kind
	Program *Program
	Tokens  []Token
	File    string
}

var useRe = regexp.MustCompile(`^use\s+(\S+)\s*(//.*)?$`)

// scanUses finds the "use" lines, which must come before any code. They are
// blanked out (keeping line numbers) because the grammar they load is not
// known until they have been fetched.
func scanUses(src string) ([]string, string, error) {
	lines := strings.Split(src, "\n")
	var uses []string
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		m := useRe.FindStringSubmatch(t)
		if m == nil {
			break
		}
		uses = append(uses, m[1])
		lines[i] = ""
	}
	if len(uses) == 0 {
		return nil, "", fmt.Errorf("the file does not use any packages, so it has no syntax; start it with 'use std'")
	}
	return uses, strings.Join(lines, "\n"), nil
}

func (c *Compiler) report() Reporter { return orNop(c.Report) }

// dep is a package to fetch and the reason it is needed.
type dep struct{ name, why string }

// fetchAll downloads packages level by level, in parallel. next returns the
// further packages a loaded package asks for.
func (c *Compiler) fetchAll(ctx context.Context, phase string, first []dep, next func(*pkgfile.File) []dep) (map[string]*pkgfile.File, error) {
	got := map[string]*pkgfile.File{}
	seen := map[string]bool{}
	var pending []dep
	queue := func(ds []dep) {
		for _, d := range ds {
			if !seen[d.name] {
				seen[d.name] = true
				pending = append(pending, d)
			}
		}
	}
	queue(first)
	start, bytes := time.Now(), 0
	for level := 1; len(pending) > 0; level++ {
		batch := pending
		pending = nil
		type result struct {
			f    *pkgfile.File
			size int
			took time.Duration
			err  error
		}
		results := make([]result, len(batch))
		var wg sync.WaitGroup
		for i, d := range batch {
			wg.Add(1)
			go func() {
				defer wg.Done()
				t0 := time.Now()
				src, origin, err := c.Registry.Fetch(ctx, d.name)
				r := result{size: len(src), took: time.Since(t0), err: err}
				if err == nil {
					r.f, r.err = pkgfile.Parse(d.name, origin, src)
				}
				results[i] = r
			}()
		}
		wg.Wait()
		fetches := make([]Fetch, 0, len(results))
		for i, r := range results {
			if r.err != nil {
				return nil, r.err
			}
			bytes += r.size
			fetches = append(fetches, Fetch{Name: r.f.Name, Why: batch[i].why, Origin: r.f.Origin, Size: r.size, Took: r.took})
			got[r.f.Name] = r.f
			queue(next(r.f))
		}
		c.report().Round(phase, level, fetches)
	}
	c.report().PhaseDone(phase, len(got), bytes, time.Since(start))
	return got, nil
}

func (c *Compiler) CompileFile(ctx context.Context, path string) (*Result, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return c.Compile(ctx, filepath.Base(path), string(src))
}

// Lex runs the pipeline up to tokenizing: fetch the syntax packages named by
// the use lines, build the grammar, and tokenize the rest of the file.
func (c *Compiler) Lex(ctx context.Context, file, src string) ([]Token, *Lang, map[string]*pkgfile.File, []string, error) {
	uses, body, err := scanUses(src)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("%s: %w", file, err)
	}

	// 1. front end: the packages that define the language
	c.report().Uses(file, uses)
	var first []dep
	for _, u := range uses {
		first = append(first, dep{u, "used by " + file})
	}
	front, err := c.fetchAll(ctx, "syntax", first, func(f *pkgfile.File) []dep {
		var out []dep
		for _, m := range f.Truck {
			out = append(out, dep{f.Name + "/" + m, "member of truck " + f.Name})
		}
		for _, n := range f.Needs {
			out = append(out, dep{n, "needed by " + f.Name})
		}
		return out
	})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var syntaxPkgs []*pkgfile.File
	for _, f := range front {
		if !f.IsTruck {
			syntaxPkgs = append(syntaxPkgs, f)
		}
	}
	sort.Slice(syntaxPkgs, func(i, j int) bool { return syntaxPkgs[i].Name < syntaxPkgs[j].Name })
	L, err := NewLang(syntaxPkgs)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	c.report().Grammar(GrammarInfo{
		Packages: len(syntaxPkgs), Forms: L.forms(), Infix: len(L.infix), Prefix: len(L.prefix),
		Keywords: sortedKeys(L.keywords), Symbols: L.Puncts,
	})

	toks, err := Lex(body, file, L.Puncts, false)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return toks, L, front, uses, nil
}

func (c *Compiler) Compile(ctx context.Context, file, src string) (*Result, error) {
	toks, L, front, uses, err := c.Lex(ctx, file, src)
	if err != nil {
		return nil, err
	}

	// 2. parse with the grammar those packages define
	t0 := time.Now()
	prog, err := NewParser(L, toks).ParseProgram()
	if err != nil {
		return nil, err
	}
	kinds := Kinds(prog)
	nodes := 0
	Walk(prog.Main, func(*Node) { nodes++ })
	for _, f := range prog.Funcs {
		Walk(f, func(*Node) { nodes++ })
	}
	info := ParseInfo{File: file, Tokens: len(toks), Nodes: nodes, Statements: len(prog.Main.Items), Took: time.Since(t0)}
	for _, f := range prog.Funcs {
		info.Functions = append(info.Functions, FrameInfo{f.Frame.Name, f.Frame.Params, f.Frame.Slots, f.Frame.Size()})
	}
	c.report().Parsed(info)

	// 3. back end: fetch only the target packages this program uses
	var need []dep
	for _, k := range sortedKeys(kinds) {
		need = append(need, dep{c.Target + "/" + k, "translates " + k})
	}
	backByName, err := c.fetchAll(ctx, c.Target, need, func(f *pkgfile.File) []dep {
		var out []dep
		for _, inc := range Includes(f) {
			out = append(out, dep{c.Target + "/" + inc, "included by " + f.Name})
		}
		for _, n := range f.Needs {
			out = append(out, dep{n, "needed by " + f.Name})
		}
		return out
	})
	if err != nil {
		return nil, err
	}
	back := map[string]*pkgfile.File{}
	for _, f := range backByName {
		back[f.Kind()] = f
	}

	// 4. render
	t0 = time.Now()
	g := NewGen(c.Target, back, prog)
	asm, err := g.Program(prog)
	if err != nil {
		return nil, err
	}
	var variants []VariantUse
	for v, n := range g.Chosen {
		v.Count = n
		variants = append(variants, v)
	}
	sort.Slice(variants, func(i, j int) bool {
		a, b := variants[i], variants[j]
		if a.Package != b.Package {
			return a.Package < b.Package
		}
		return a.Section+a.When < b.Section+b.When
	})
	c.report().Emitted(strings.Count(asm, "\n"), time.Since(t0), variants)
	return &Result{Asm: asm, Uses: uses, Front: front, Back: back, Program: prog, Tokens: toks, File: file}, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Assemble turns NASM source into an executable with nasm and ld. r may be
// nil.
func Assemble(asm, out string, r Reporter) error {
	r = orNop(r)
	dir, err := os.MkdirTemp("", "packenger-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	asmPath, objPath := filepath.Join(dir, "prog.asm"), filepath.Join(dir, "prog.o")
	if err := os.WriteFile(asmPath, []byte(asm), 0o644); err != nil {
		return err
	}
	for _, tool := range [][]string{
		{"nasm", "-f", "elf64", "-o", objPath, asmPath},
		{"ld", "-o", out, objPath},
	} {
		if _, err := exec.LookPath(tool[0]); err != nil {
			return fmt.Errorf("%s is not installed (packenger needs nasm and ld)", tool[0])
		}
		t0 := time.Now()
		if b, err := exec.Command(tool[0], tool[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s failed: %v\n%s", tool[0], err, b)
		}
		r.Tool(tool[0], time.Since(t0))
	}
	if st, err := os.Stat(out); err == nil {
		r.Built(out, st.Size())
	}
	return nil
}

// Tree prints which packages built the program.
func (r *Result) Tree(w io.Writer, target string) {
	used := map[string]int{}
	expanded := map[string]bool{}
	count := func(n *Node) {
		used[n.Kind]++
		for _, e := range n.Expanded {
			expanded[e] = true
		}
	}
	Walk(r.Program.Main, count)
	for _, f := range r.Program.Funcs {
		Walk(f, count)
	}
	fmt.Fprintln(w, r.File)
	var backLine func(prefix, kind string, seen map[string]bool)
	backLine = func(prefix, kind string, seen map[string]bool) {
		b := r.Back[kind]
		if b == nil {
			return
		}
		incs := Includes(b)
		for i, inc := range incs {
			branch, next := "├── ", "│   "
			if i == len(incs)-1 {
				branch, next = "└── ", "    "
			}
			fmt.Fprintf(w, "%s%s@%s/%s\n", prefix, branch, target, inc)
			if !seen[inc] {
				seen[inc] = true
				backLine(prefix+next, inc, seen)
			}
		}
	}
	type row struct{ label, kind, note string }
	var rows []row
	names := make([]string, 0, len(r.Front))
	for n := range r.Front {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f := r.Front[n]
		if f.IsTruck {
			continue
		}
		k := f.Kind()
		switch {
		case expanded[n]:
			rows = append(rows, row{n, "", "expands into other packages"})
		case used[k] > 0:
			rows = append(rows, row{n, k, ""})
		}
	}
	for _, core := range []string{"int", "str", "var"} {
		if used[core] > 0 {
			rows = append(rows, row{map[string]string{"int": "(integer)", "str": "(string)", "var": "(variable)"}[core], core, ""})
		}
	}
	rows = append(rows, row{"(program)", "program", ""})
	for i, rw := range rows {
		branch, next := "├── ", "│   "
		if i == len(rows)-1 {
			branch, next = "└── ", "    "
		}
		switch {
		case rw.note != "":
			fmt.Fprintf(w, "%s%-16s %s\n", branch, rw.label, rw.note)
		default:
			fmt.Fprintf(w, "%s%-16s → %s/%s\n", branch, rw.label, target, rw.kind)
			backLine(next, rw.kind, map[string]bool{})
		}
	}
}

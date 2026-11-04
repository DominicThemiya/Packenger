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

	"github.com/DominicThemiya/Packenger/internal/pkgfile"
	"github.com/DominicThemiya/Packenger/internal/registry"
)

type Compiler struct {
	Registry *registry.Registry
	Target   string // backend truck, e.g. "bintables"
	Log      func(format string, args ...any)
}

type Result struct {
	Asm     string
	Uses    []string
	Front   map[string]*pkgfile.File // every front-end package loaded, by name
	Back    map[string]*pkgfile.File // backend packages, by kind
	Program *Program
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

func (c *Compiler) log(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

// fetchAll downloads packages level by level, in parallel. next returns the
// further package names a loaded package asks for.
func (c *Compiler) fetchAll(ctx context.Context, names []string, next func(*pkgfile.File) []string) (map[string]*pkgfile.File, error) {
	got := map[string]*pkgfile.File{}
	seen := map[string]bool{}
	var pending []string
	queue := func(ns []string) {
		for _, n := range ns {
			if !seen[n] {
				seen[n] = true
				pending = append(pending, n)
			}
		}
	}
	queue(names)
	for len(pending) > 0 {
		level := pending
		pending = nil
		files := make([]*pkgfile.File, len(level))
		errs := make([]error, len(level))
		var wg sync.WaitGroup
		for i, name := range level {
			wg.Add(1)
			go func() {
				defer wg.Done()
				src, origin, err := c.Registry.Fetch(ctx, name)
				if err != nil {
					errs[i] = err
					return
				}
				files[i], errs[i] = pkgfile.Parse(name, origin, src)
			}()
		}
		wg.Wait()
		for i, f := range files {
			if errs[i] != nil {
				return nil, errs[i]
			}
			c.log("fetched %-22s %s", f.Name, f.Origin)
			got[f.Name] = f
			queue(next(f))
		}
	}
	return got, nil
}

func (c *Compiler) CompileFile(ctx context.Context, path string) (*Result, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return c.Compile(ctx, filepath.Base(path), string(src))
}

func (c *Compiler) Compile(ctx context.Context, file, src string) (*Result, error) {
	uses, body, err := scanUses(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}

	// 1. front end: the packages that define the language
	front, err := c.fetchAll(ctx, uses, func(f *pkgfile.File) []string {
		var out []string
		for _, m := range f.Truck {
			out = append(out, f.Name+"/"+m)
		}
		return append(out, f.Needs...)
	})
	if err != nil {
		return nil, err
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
		return nil, err
	}

	// 2. parse with the grammar those packages define
	toks, err := Lex(body, file, L.Puncts, false)
	if err != nil {
		return nil, err
	}
	prog, err := NewParser(L, toks).ParseProgram()
	if err != nil {
		return nil, err
	}

	// 3. back end: fetch only the target packages this program uses
	var need []string
	for k := range Kinds(prog) {
		need = append(need, c.Target+"/"+k)
	}
	sort.Strings(need)
	backByName, err := c.fetchAll(ctx, need, func(f *pkgfile.File) []string {
		var out []string
		for _, inc := range Includes(f) {
			out = append(out, c.Target+"/"+inc)
		}
		return append(out, f.Needs...)
	})
	if err != nil {
		return nil, err
	}
	back := map[string]*pkgfile.File{}
	for _, f := range backByName {
		back[f.Kind()] = f
	}

	// 4. render
	asm, err := NewGen(c.Target, back, prog).Program(prog)
	if err != nil {
		return nil, err
	}
	return &Result{Asm: asm, Uses: uses, Front: front, Back: back, Program: prog, File: file}, nil
}

// Assemble turns NASM source into an executable with nasm and ld.
func Assemble(asm, out string) error {
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
		if b, err := exec.Command(tool[0], tool[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s failed: %v\n%s", tool[0], err, b)
		}
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

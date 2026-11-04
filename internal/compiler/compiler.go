// Package compiler turns a Packenger source file into a bit string.
//
// Pipeline:
//
//  1. parse the local file;
//  2. traverse the package tree, downloading every referenced package from
//     the registry (level by level, in parallel; nothing is stored on disk);
//  3. compile each package once to a combinator tree whose leaves are other
//     packages, eliminating lambdas into the hosted S, K and I packages;
//  4. unwrap: walk the tree down to the bootstrap packages 0 and 1, emitting
//     application f x as  1 ++ bits(f) ++ bits(x).
//
// The only things the compiler knows natively are the bits 0 and 1,
// application, and that lambda elimination targets packages named S, K, I.
package compiler

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/DominicThemiya/Packenger/internal/machine"
	"github.com/DominicThemiya/Packenger/internal/registry"
	"github.com/DominicThemiya/Packenger/internal/syntax"
)

// Term is a package body after lambda elimination.
type Term struct {
	Kind termKind
	L, R *Term
	Name string // var name
	Key  string // referenced unit
	Bits string
}

type termKind int

const (
	kApp termKind = iota
	kVar
	kRef
	kBits
)

// Unit is one compiled package: a hosted package, the local main file, or a
// file-local helper (key "<file>#<name>").
type Unit struct {
	Key    string
	Origin string
	Term   *Term
	Deps   []string // unit keys, in order of first use
	Raw    string   // set when the body is nothing but bits
}

type Compiler struct {
	Registry *registry.Registry
	Log      func(format string, args ...any)

	mu        sync.Mutex
	units     map[string]*Unit
	requested map[string]bool
	bits      map[string]string
}

func New(reg *registry.Registry) *Compiler {
	return &Compiler{
		Registry:  reg,
		Log:       func(string, ...any) {},
		units:     map[string]*Unit{},
		requested: map[string]bool{},
		bits:      map[string]string{},
	}
}

type Program struct {
	Root     string
	Bits     string
	Output   string // "# output:" directive of the main file, if any
	Units    map[string]*Unit
	UnitBits map[string]string
	Fetched  int
}

func (c *Compiler) CompileFile(ctx context.Context, path string) (*Program, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return c.CompileSource(ctx, filepath.Base(path), string(src))
}

func (c *Compiler) CompileSource(ctx context.Context, name, src string) (*Program, error) {
	f, err := syntax.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%s:%v", name, err)
	}
	root := "./" + name
	need, err := c.register(root, name, f)
	if err != nil {
		return nil, err
	}
	if err := c.resolve(ctx, need); err != nil {
		return nil, err
	}
	bits, err := c.emit(root, nil)
	if err != nil {
		return nil, err
	}
	return &Program{Root: root, Bits: bits, Output: f.Directives["output"], Units: c.units, UnitBits: c.bits, Fetched: len(c.requested)}, nil
}

// ---------------------------------------------------------------- traversal

type fetched struct {
	name, src, origin string
	err               error
}

func (c *Compiler) resolve(ctx context.Context, names []string) error {
	pending := c.newNames(names)
	for len(pending) > 0 {
		results := make([]fetched, len(pending))
		var wg sync.WaitGroup
		for i, n := range pending {
			wg.Add(1)
			go func() {
				defer wg.Done()
				src, origin, err := c.Registry.Fetch(ctx, n)
				results[i] = fetched{n, src, origin, err}
			}()
		}
		wg.Wait()
		var next []string
		for _, r := range results {
			if r.err != nil {
				return r.err
			}
			c.Log("fetched %-12s %s", r.name, r.origin)
			f, err := syntax.Parse(r.src)
			if err != nil {
				return fmt.Errorf("package %s (%s):%v", r.name, r.origin, err)
			}
			need, err := c.register(r.name, r.origin, f)
			if err != nil {
				return err
			}
			next = append(next, c.newNames(need)...)
		}
		pending = next
	}
	return nil
}

func (c *Compiler) newNames(names []string) []string {
	var out []string
	for _, n := range names {
		if !c.requested[n] {
			c.requested[n] = true
			out = append(out, n)
		}
	}
	return out
}

// register compiles every package in a file and returns the hosted package
// names it references.
func (c *Compiler) register(key, origin string, f *syntax.File) ([]string, error) {
	scope := map[string]string{}
	for _, d := range f.Defs {
		scope[d.Name] = key + "#" + d.Name
	}
	var need []string
	add := func(ukey string, body syntax.Expr) error {
		u := &Unit{Key: ukey, Origin: origin}
		if b, ok := body.(*syntax.Bits); ok {
			u.Raw = b.Bits
		}
		t, err := c.toTerm(body, nil, scope, origin)
		if err != nil {
			return err
		}
		u.Term = t
		seen := map[string]bool{}
		walkRefs(t, func(k string) {
			if !seen[k] {
				seen[k] = true
				u.Deps = append(u.Deps, k)
				if !strings.Contains(k, "#") && !strings.HasPrefix(k, "./") {
					need = append(need, k)
				}
			}
		})
		c.units[ukey] = u
		return nil
	}
	for _, d := range f.Defs {
		if err := add(scope[d.Name], d.Body); err != nil {
			return nil, err
		}
	}
	if err := add(key, f.Body); err != nil {
		return nil, err
	}
	return need, nil
}

func walkRefs(t *Term, fn func(string)) {
	switch t.Kind {
	case kApp:
		walkRefs(t.L, fn)
		walkRefs(t.R, fn)
	case kRef:
		fn(t.Key)
	}
}

// ---------------------------------------------------------------- lambda elimination

func app(l, r *Term) *Term { return &Term{Kind: kApp, L: l, R: r} }
func ref(key string) *Term { return &Term{Kind: kRef, Key: key} }

func (c *Compiler) toTerm(e syntax.Expr, bound []string, scope map[string]string, origin string) (*Term, error) {
	switch e := e.(type) {
	case *syntax.Ident:
		if !e.Global {
			for i := len(bound) - 1; i >= 0; i-- {
				if bound[i] == e.Name {
					return &Term{Kind: kVar, Name: e.Name}, nil
				}
			}
			if k, ok := scope[e.Name]; ok {
				return ref(k), nil
			}
		}
		if !registry.ValidName(e.Name) {
			return nil, fmt.Errorf("%s:%s: %q is not a valid package name", origin, e.P, e.Name)
		}
		return ref(e.Name), nil
	case *syntax.Apply:
		f, err := c.toTerm(e.Fn, bound, scope, origin)
		if err != nil {
			return nil, err
		}
		a, err := c.toTerm(e.Arg, bound, scope, origin)
		if err != nil {
			return nil, err
		}
		return app(f, a), nil
	case *syntax.Lambda:
		body, err := c.toTerm(e.Body, append(bound, e.Param), scope, origin)
		if err != nil {
			return nil, err
		}
		return abstract(e.Param, body), nil
	case *syntax.Bits:
		if !machine.IsApplicative(e.Bits) {
			return nil, fmt.Errorf("%s:%s: @%s is not an applicative bit string (appending it must apply the program to one combinator)", origin, e.P, e.Bits)
		}
		return &Term{Kind: kBits, Bits: e.Bits}, nil
	}
	panic("unreachable")
}

func free(x string, t *Term) bool {
	switch t.Kind {
	case kVar:
		return t.Name == x
	case kApp:
		return free(x, t.L) || free(x, t.R)
	}
	return false
}

// abstract computes [x]t (bracket abstraction with the K and η rules).
func abstract(x string, t *Term) *Term {
	switch {
	case !free(x, t):
		return app(ref("K"), t)
	case t.Kind == kVar:
		return ref("I")
	case t.R.Kind == kVar && t.R.Name == x && !free(x, t.L):
		return t.L
	}
	return app(app(ref("S"), abstract(x, t.L)), abstract(x, t.R))
}

// ---------------------------------------------------------------- unwrapping

func (c *Compiler) emit(key string, stack []string) (string, error) {
	if b, ok := c.bits[key]; ok {
		return b, nil
	}
	for i, k := range stack {
		if k == key {
			cyc := append(append([]string{}, stack[i:]...), key)
			return "", fmt.Errorf("cyclic package dependency: %s\n(packages are unwrapped by inlining, so recursion must go through the Y package)", strings.Join(displayAll(cyc), " -> "))
		}
	}
	u, ok := c.units[key]
	if !ok {
		return "", fmt.Errorf("internal: unit %q was never loaded", key)
	}
	stack = append(stack, key)
	var sb strings.Builder
	var walk func(t *Term) error
	walk = func(t *Term) error {
		switch t.Kind {
		case kApp:
			sb.WriteByte('1')
			if err := walk(t.L); err != nil {
				return err
			}
			return walk(t.R)
		case kRef:
			b, err := c.emit(t.Key, stack)
			if err != nil {
				return err
			}
			sb.WriteString(b)
		case kBits:
			sb.WriteString(t.Bits)
		case kVar:
			return fmt.Errorf("internal: free variable %s in %s", t.Name, key)
		}
		return nil
	}
	if err := walk(u.Term); err != nil {
		return "", err
	}
	c.bits[key] = sb.String()
	return c.bits[key], nil
}

// Display renders a unit key for humans.
func Display(key string) string {
	if p, n, ok := strings.Cut(key, "#"); ok {
		return Display(p) + "#" + n
	}
	return strings.TrimPrefix(key, "./")
}

func displayAll(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = Display(k)
	}
	return out
}

// ---------------------------------------------------------------- reporting

// Tree prints the package tree down to the bootstrap packages.
func (p *Program) Tree(w io.Writer) {
	shown := map[string]bool{}
	var walk func(key, prefix string, last bool, top bool)
	walk = func(key, prefix string, last, top bool) {
		u := p.Units[key]
		branch, childPrefix := "├── ", prefix+"│   "
		if last {
			branch, childPrefix = "└── ", prefix+"    "
		}
		if top {
			branch, childPrefix = "", ""
		}
		label := Display(key)
		size := len(p.UnitBits[key])
		switch {
		case u.Raw != "":
			fmt.Fprintf(w, "%s%s%s = %s  [bootstrap bits]\n", prefix, branch, label, strings.Join(strings.Split(u.Raw, ""), " "))
			return
		case shown[key]:
			fmt.Fprintf(w, "%s%s%s  (%d bits, see above)\n", prefix, branch, label, size)
			return
		}
		shown[key] = true
		fmt.Fprintf(w, "%s%s%s  (%d bits)\n", prefix, branch, label, size)
		for i, d := range u.Deps {
			walk(d, childPrefix, i == len(u.Deps)-1, false)
		}
	}
	walk(p.Root, "", true, true)
}

// Packages lists the hosted packages the program was built from.
func (p *Program) Packages() []string {
	var out []string
	for k := range p.Units {
		if !strings.Contains(k, "#") && !strings.HasPrefix(k, "./") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

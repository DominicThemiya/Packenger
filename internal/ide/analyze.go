// Package ide is the engine of pkide, the Packenger IDE: it analyzes
// programs and package files as they are edited and answers completion,
// hover, definition and diagnostics requests, using the compiler's own
// grammar loading and parser (internal/lang).
package ide

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/DominicThemiya/Packenger/internal/lang"
	"github.com/DominicThemiya/Packenger/internal/pkgfile"
	"github.com/DominicThemiya/Packenger/internal/registry"
)

// sectionKeywords start the sections of a package file.
var sectionKeywords = []string{"doc", "needs", "type", "truck", "syntax", "expand", "code", "emit", "data", "support", "resolve"}

// IsProgram reports whether src is a program (it starts with use lines)
// rather than a package file (it starts with sections).
func IsProgram(src string) bool {
	for _, l := range strings.Split(src, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		return t == "use" || strings.HasPrefix(t, "use ") || strings.HasPrefix(t, "use\t")
	}
	return false
}

// Engine analyzes source files. It caches registries (and so downloaded
// packages) for a while, and the last analysis of each file that parsed.
type Engine struct {
	// DefaultRegistry is used when a file has no pkg_tree and no registry
	// is chosen (PACKENGER_REGISTRY, else the universal tree).
	DefaultRegistry string

	mu    sync.Mutex
	regs  map[string]*cachedReg
	good  map[string]*lang.Analysis // file path -> last analysis that parsed
	index map[string]*cachedIndex
}

type cachedReg struct {
	r       *registry.Registry
	created time.Time
}

type cachedIndex struct {
	names   []string
	created time.Time
}

func NewEngine(defaultRegistry string) *Engine {
	return &Engine{DefaultRegistry: defaultRegistry, regs: map[string]*cachedReg{}, good: map[string]*lang.Analysis{}, index: map[string]*cachedIndex{}}
}

// Forget drops every cached package and analysis, so the next request
// downloads packages again.
func (e *Engine) Forget() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.regs = map[string]*cachedReg{}
	e.index = map[string]*cachedIndex{}
}

// registryFor returns a registry for base with routes, shared between
// requests: for a minute if it reads local files (which are being edited),
// else for ten.
func (e *Engine) registryFor(base string, routes []registry.Route) *registry.Registry {
	key := base
	for _, rt := range routes {
		key += "|" + rt.Prefix + "=" + rt.Base
	}
	ttl := 10 * time.Minute
	if strings.HasPrefix(base, "file://") {
		ttl = 3 * time.Second
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.regs[key]; ok && time.Since(c.created) < ttl {
		return c.r
	}
	r := registry.New(base)
	r.Routes = routes
	e.regs[key] = &cachedReg{r, time.Now()}
	return r
}

// Source is a file being edited.
type Source struct {
	Path     string // absolute path on disk (it may not be saved yet)
	Text     string
	Registry string // the registry chosen for the workspace; "" for the default
}

// registry returns the registry the compiler would use for src: the chosen
// one, else its pkg_tree.pkg routes, else the default.
func (e *Engine) registry(src Source) *registry.Registry {
	if src.Registry != "" {
		return e.registryFor(src.Registry, nil)
	}
	base := e.DefaultRegistry
	var routes []registry.Route
	if lang.UsesTree(src.Text) {
		if tree, err := registry.ReadTree(src.Path); err == nil {
			routes = tree.Routes
			if tree.Default != "" {
				base = tree.Default
			}
		}
	}
	return e.registryFor(base, routes)
}

func (e *Engine) analyzeText(ctx context.Context, src Source, text string) (*lang.Analysis, error) {
	c := &lang.Compiler{Registry: e.registry(src), Target: "bintables"}
	return c.Analyze(ctx, filepath.Base(src.Path), text)
}

// A View is what the engine knows about a file at one point: the grammar,
// and the syntax tree of the best version of the file that parsed.
type View struct {
	Src     Source
	Program bool // a program; else a package file
	A       *lang.Analysis
	// Offset is added to a line of the file to get its line in the analyzed
	// text (a package file's code section is analyzed as a program with use
	// lines in front).
	Offset int
	Pkg    *pkgfile.File // a package file, parsed (nil if it doesn't parse)
	PkgErr error
}

// Analyze looks at src with the cursor on line (0-based). The line being
// edited is left out, so that what is half-written doesn't stop the rest
// from parsing; if the file still doesn't parse (a block whose end isn't
// written yet), the text up to the cursor is tried with its open blocks
// closed, and then the last version of the file that parsed.
func (e *Engine) Analyze(ctx context.Context, src Source, line int) (*View, error) {
	v := &View{Src: src, Program: IsProgram(src.Text)}
	text := src.Text
	if !v.Program {
		v.Pkg, v.PkgErr = pkgfile.Parse(pkgName(src.Path), src.Path, src.Text)
		var ok bool
		text, v.Offset, ok = codeAsProgram(src.Text)
		if !ok {
			return v, nil
		}
	}
	lines := strings.Split(text, "\n")
	at := line + v.Offset
	blank := func(ls []string, i int) []string {
		out := append([]string(nil), ls...)
		if i >= 0 && i < len(out) && !isUseLine(out[i]) {
			out[i] = ""
		}
		return out
	}
	cur := blank(lines, at)
	a, err := e.analyzeText(ctx, src, strings.Join(cur, "\n"))
	if err != nil {
		return v, err
	}
	// a mistake elsewhere in the file shouldn't hide what's in scope: leave
	// out the lines the parser stops at, a few times
	name := filepath.Base(src.Path)
	for i := 0; i < 6 && a.Program == nil && a.Err != nil; i++ {
		d := diagnose(a.Err, name, 0)
		if d.File != "" || d.Line < 1 || d.Line > len(cur) || strings.TrimSpace(cur[d.Line-1]) == "" || isUseLine(cur[d.Line-1]) {
			break
		}
		cur[d.Line-1] = ""
		b, err := e.analyzeText(ctx, src, strings.Join(cur, "\n"))
		if err != nil {
			break
		}
		a = b
	}
	v.A = a
	if a.Program == nil && at < len(lines) {
		// close the blocks open at the cursor and try again
		head := blank(lines[:min(at+1, len(lines))], at)
		depth := openBlocks(a.Lang, a.Front, head)
		if depth > 0 {
			b, err := e.analyzeText(ctx, src, strings.Join(head, "\n")+strings.Repeat("\nend", depth)+"\n")
			if err == nil && b.Program != nil {
				v.A = b
			}
		}
	}
	key := src.Path
	e.mu.Lock()
	defer e.mu.Unlock()
	if v.A.Program != nil {
		e.good[key] = v.A
	} else if g := e.good[key]; g != nil {
		// keep the grammar of now, the tree of the last good version
		v.A = &lang.Analysis{Uses: a.Uses, Front: a.Front, Lang: a.Lang, Program: g.Program, Err: a.Err}
	}
	return v, nil
}

func isUseLine(l string) bool {
	t := strings.TrimSpace(l)
	return t == "use" || strings.HasPrefix(t, "use ")
}

// pkgName guesses a package file's name from its path: the part after the
// last registry/ folder, else the file name.
func pkgName(path string) string {
	p := filepath.ToSlash(path)
	if i := strings.LastIndex(p, "registry/"); i >= 0 {
		p = p[i+len("registry/"):]
	} else {
		p = filepath.Base(p)
	}
	return strings.TrimSuffix(p, ".pkg")
}

// codeAsProgram turns a package file's code section into a program with
// the same line numbers: use lines for its needs, then the code, with
// every other line blank. offset is how many lines were added in front
// when there wasn't room for the use lines.
func codeAsProgram(src string) (text string, offset int, ok bool) {
	lines := strings.Split(src, "\n")
	var needs []string
	code, stop := -1, len(lines)
	for i, l := range lines {
		if l == "" || l[0] == ' ' || l[0] == '\t' {
			continue
		}
		f := strings.Fields(l)
		if code >= 0 && i > code && stop == len(lines) {
			stop = i
		}
		switch f[0] {
		case "needs":
			needs = append(needs, f[1:]...)
		case "code":
			code = i
		}
	}
	if code < 0 {
		return "", 0, false
	}
	uses := []string{}
	for _, n := range needs {
		uses = append(uses, "use "+n)
	}
	if len(uses) == 0 {
		uses = append(uses, "use std")
	}
	out := make([]string, len(lines))
	copy(out[code+1:stop], lines[code+1:stop])
	if len(uses) > code {
		offset = len(uses) - code
		out = append(make([]string, offset), out...)
	}
	copy(out, uses)
	return strings.Join(out, "\n"), offset, true
}

// grammarInfo is what layout and completion need to know about block
// structure: which words open a block, which close it, which reopen it.
type grammarInfo struct {
	openers map[string]bool
	closers map[string]bool
	reopen  map[string]bool
	args    map[string]bool // closers followed by more on their line (elif x)
}

func isBlock(e pkgfile.Elem) bool {
	return e.Slot != nil && (e.Slot.Kind == "block" || e.Slot.Kind == "fields")
}

func blockInfo(front map[string]*pkgfile.File) *grammarInfo {
	g := &grammarInfo{openers: map[string]bool{}, closers: map[string]bool{}, reopen: map[string]bool{}, args: map[string]bool{}}
	var walk func(es []pkgfile.Elem, closers map[string]bool)
	walk = func(es []pkgfile.Elem, closers map[string]bool) {
		for i, e := range es {
			if e.Optional != nil {
				walk(e.Optional, closers)
			}
			if e.Lit == "" || !closers[e.Lit] {
				continue
			}
			for _, later := range es[i+1:] {
				if isBlock(later) {
					g.reopen[e.Lit] = true
				}
			}
			if i+1 < len(es) && es[i+1].Slot != nil && !isBlock(es[i+1]) {
				g.args[e.Lit] = true
			}
		}
	}
	for _, f := range front {
		for _, s := range f.Syntax {
			for _, p := range s.Patterns {
				closers := map[string]bool{}
				for _, c := range p.Closers() {
					closers[c] = true
					g.closers[c] = true
				}
				statement := s.Kind != "expr" && s.Kind != "infix" && s.Kind != "prefix" && s.Kind != "postfix"
				if first := p.Elems[0]; statement && first.Lit != "" && isWordLit(first.Lit) {
					for _, e := range p.Elems {
						if isBlock(e) {
							g.openers[first.Lit] = true
						}
					}
				}
				walk(p.Elems, closers)
			}
		}
	}
	return g
}

var wordRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func isWordLit(s string) bool { return wordRe.MatchString(s) }

// openBlocks counts the blocks still open at the end of lines.
func openBlocks(L *lang.Lang, front map[string]*pkgfile.File, lines []string) int {
	g := blockInfo(front)
	depth := 0
	for _, l := range lines {
		f := strings.Fields(strings.SplitN(l, "//", 2)[0])
		if len(f) == 0 {
			continue
		}
		w := f[0]
		switch {
		case g.closers[w] && (len(f) == 1 || g.args[w]):
			if depth > 0 {
				depth--
			}
			if g.reopen[w] {
				depth++
			}
		case g.openers[w] && !(len(f) > 1 && f[1] == "="):
			depth++
		}
	}
	return depth
}

// Diagnostic is a problem in a file, with a 1-based line and column (0 if
// unknown).
type Diagnostic struct {
	Line int    `json:"line"`
	Col  int    `json:"col"`
	Msg  string `json:"msg"`
	File string `json:"file,omitempty"` // set when the problem is in another file (a package)
}

var posRe = regexp.MustCompile(`^(.*?):(\d+)(?::(\d+))?: (.*)$`)

// diagnose turns a compiler error into a diagnostic for the file.
func diagnose(err error, file string, offset int) Diagnostic {
	msg := err.Error()
	m := posRe.FindStringSubmatch(msg)
	if m == nil {
		return Diagnostic{Line: 1, Msg: msg}
	}
	line, col := atoi(m[2]), atoi(m[3])
	where := m[1]
	if filepath.Base(where) != filepath.Base(file) {
		return Diagnostic{Line: 1, Msg: msg, File: where}
	}
	return Diagnostic{Line: line - offset, Col: col, Msg: m[4]}
}

func atoi(s string) int {
	n := 0
	fmt.Sscan(s, &n)
	return n
}

// Check returns the problems in a file, as the compiler sees it (without
// leaving out the line being edited).
func (e *Engine) Check(ctx context.Context, src Source) []Diagnostic {
	if !IsProgram(src.Text) {
		if _, err := pkgfile.Parse(pkgName(src.Path), src.Path, src.Text); err != nil {
			return []Diagnostic{diagnose(err, src.Path, 0)}
		}
		text, offset, ok := codeAsProgram(src.Text)
		if !ok {
			return nil
		}
		a, err := e.analyzeText(ctx, src, text)
		if err != nil {
			return []Diagnostic{{Line: 1, Msg: err.Error()}}
		}
		if a.Err != nil {
			return []Diagnostic{diagnose(a.Err, src.Path, offset)}
		}
		return nil
	}
	a, err := e.analyzeText(ctx, src, src.Text)
	if err != nil {
		d := diagnose(err, src.Path, 0)
		if d.File != "" || d.Line == 0 {
			d.Line = 1
		}
		return []Diagnostic{d}
	}
	if a.Err != nil {
		return []Diagnostic{diagnose(a.Err, src.Path, 0)}
	}
	e.mu.Lock()
	e.good[src.Path] = a
	e.mu.Unlock()
	return nil
}

// PackageNames lists the packages a registry offers: its index.txt, or for
// a file:// registry, the .pkg files under it.
func (e *Engine) PackageNames(ctx context.Context, base string) []string {
	e.mu.Lock()
	if c, ok := e.index[base]; ok && time.Since(c.created) < 5*time.Minute {
		e.mu.Unlock()
		return c.names
	}
	e.mu.Unlock()
	var names []string
	if dir, ok := strings.CutPrefix(base, "file://"); ok {
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".pkg") {
				rel, _ := filepath.Rel(dir, p)
				names = append(names, strings.TrimSuffix(filepath.ToSlash(rel), ".pkg"))
			}
			return nil
		})
	} else {
		if txt, err := httpGet(ctx, strings.TrimRight(base, "/")+"/index.txt"); err == nil {
			for _, l := range strings.Split(txt, "\n") {
				if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
					names = append(names, strings.Fields(l)[0])
				}
			}
		}
	}
	e.mu.Lock()
	e.index[base] = &cachedIndex{names, time.Now()}
	e.mu.Unlock()
	return names
}

func httpGet(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "pkide")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return string(b), err
}

// FetchPackage returns a package's source and where it came from, through
// the registry src would use.
func (e *Engine) FetchPackage(ctx context.Context, src Source, name string) (string, string, error) {
	return e.registry(src).Fetch(ctx, name)
}

// RegistryBase is the registry src's packages come from by default.
func (e *Engine) RegistryBase(src Source) string { return e.registry(src).Base }

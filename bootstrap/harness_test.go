// Package bootstrap holds the comparison harness for self-hosting: it runs
// every compiler under test on the same inputs and requires identical
// output at every stage. See docs/self-hosting.md and docs/dumps.md.
//
// Compilers under test:
//
//   - the Go compiler, built from this repository;
//   - pkc, if the PKC environment variable names its executable. While pkc
//     is being ported, PKC_STAGES limits it to the stages it supports, as a
//     comma-separated list of pkg, tokens, ast, asm, run and report (-vb
//     and tree).
//
// The corpus is the test programs here, the examples and the rt truck's
// tests. A program containing the line "// output: varies" is compiled but
// its run is not compared.
//
// Every compiler is run several times per input, so nondeterminism (for
// example from Go map iteration) fails the harness even with one compiler.
package bootstrap

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const runs = 3

type compiler struct {
	name, bin string
	stages    map[string]bool // nil: every stage
}

var (
	compilers []compiler
	registry  string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "packenger-harness-*")
	if err != nil {
		panic(err)
	}
	goBin := filepath.Join(dir, "packenger")
	if out, err := exec.Command("go", "build", "-o", goBin, "../cmd/packenger").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building the Go compiler: %v\n%s", err, out)
		os.Exit(1)
	}
	compilers = append(compilers, compiler{name: "go", bin: goBin})
	if pkc := os.Getenv("PKC"); pkc != "" {
		c := compiler{name: "pkc", bin: pkc}
		if st := os.Getenv("PKC_STAGES"); st != "" {
			c.stages = map[string]bool{}
			for _, s := range strings.Split(st, ",") {
				c.stages[strings.TrimSpace(s)] = true
			}
		}
		compilers = append(compilers, c)
	}
	reg, _ := filepath.Abs("../registry")
	registry = "file://" + reg
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type outcome struct {
	stdout, stderr string
	exit           int
}

func (o outcome) String() string {
	return fmt.Sprintf("exit %d\n--- stdout\n%s--- stderr\n%s", o.exit, o.stdout, o.stderr)
}

func run(t *testing.T, bin string, args ...string) outcome {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "PACKENGER_REGISTRY="+registry)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	o := outcome{stdout: stdout.String(), stderr: stderr.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		o.exit = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running %s: %v", bin, err)
	}
	return o
}

// agree runs args on every compiler, runs times each, and requires all
// outcomes to be identical. It returns the agreed outcome.
func agree(t *testing.T, args ...string) outcome {
	t.Helper()
	return agreeIn(t, "", func(s string) string { return s }, args...)
}

var (
	timeRe  = regexp.MustCompile(`[0-9]+(\.[0-9]+)? (µs|ms|s)\b`)
	barRe   = regexp.MustCompile(`[█▏▎▍▌▋▊▉]+`)
	tmpRe   = regexp.MustCompile(`/tmp/packenger-[^ \n]*`)
	spaceRe = regexp.MustCompile(` +`)
)

// unTimed masks what differs between two runs of a -vb report: timings,
// the timing bars, temporary paths and the padding around them.
func unTimed(s string) string {
	s = timeRe.ReplaceAllString(s, "<time>")
	s = barRe.ReplaceAllString(s, "")
	s = tmpRe.ReplaceAllString(s, "<tmp>")
	return spaceRe.ReplaceAllString(s, " ")
}

func agreeIn(t *testing.T, stage string, norm func(string) string, args ...string) outcome {
	t.Helper()
	var first outcome
	var firstName string
	if stage == "" {
		stage = args[0]
		if stage == "dump" && len(args) > 1 {
			stage = args[1]
		}
		switch stage {
		case "tokens", "ast", "asm", "run":
		default:
			stage = "pkg" // package files and command-line handling
		}
	}
	for _, c := range compilers {
		if c.stages != nil && !c.stages[stage] {
			continue
		}
		for i := 1; i <= runs; i++ {
			o := run(t, c.bin, args...)
			o.stdout, o.stderr = norm(o.stdout), norm(o.stderr)
			name := fmt.Sprintf("%s run %d", c.name, i)
			if firstName == "" {
				first, firstName = o, name
				continue
			}
			if o != first {
				t.Fatalf("%s differs from %s for %q:\n%s\n%s:\n%s\n%s:\n%s",
					name, firstName, strings.Join(args, " "), diff(first.String(), o.String()),
					firstName, first, name, o)
			}
		}
	}
	return first
}

// diff points at the first differing line.
func diff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < max(len(al), len(bl)); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return fmt.Sprintf("first difference at line %d:\n  - %q\n  + %q", i+1, x, y)
		}
	}
	return "(no line differs)"
}

func corpus(t *testing.T, globs ...string) []string {
	var files []string
	for _, g := range globs {
		m, err := filepath.Glob(g)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatalf("empty corpus %v", globs)
	}
	return files
}

// Programs that compile: every stage must agree, and so must running them.
func TestPrograms(t *testing.T) {
	if _, err := exec.LookPath("nasm"); err != nil {
		t.Skip("nasm not installed")
	}
	for _, f := range corpus(t, "testdata/programs/*.pkg", "../examples/*.pkg", "../tests/rt/*.pkg") {
		t.Run(filepath.Base(f), func(t *testing.T) {
			for _, stage := range []string{"tokens", "ast", "asm"} {
				if o := agree(t, "dump", stage, f); o.exit != 0 {
					t.Fatalf("dump %s failed:\n%s", stage, o)
				}
			}
			// programs whose output can't repeat (timings) are marked
			src, _ := os.ReadFile(f)
			if !strings.Contains(string(src), "// output: varies") {
				agree(t, "run", f)
			}
		})
	}
}

// Programs that must not compile: the error text and exit code must agree,
// and so must every stage that still succeeds.
func TestErrors(t *testing.T) {
	for _, f := range corpus(t, "testdata/errors/*.pkg") {
		t.Run(filepath.Base(f), func(t *testing.T) {
			failed := false
			for _, stage := range []string{"tokens", "ast", "asm"} {
				failed = agree(t, "dump", stage, f).exit != 0 || failed
			}
			if !failed {
				t.Fatalf("%s compiled, but it is in the error corpus", f)
			}
		})
	}
}

// Every registry package must be parsed identically.
func TestPackages(t *testing.T) {
	var names []string
	filepath.WalkDir("../registry", func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".pkg") {
			rel, _ := filepath.Rel("../registry", p)
			names = append(names, strings.TrimSuffix(filepath.ToSlash(rel), ".pkg"))
		}
		return nil
	})
	sort.Strings(names)
	for _, n := range names {
		if o := agree(t, "dump", "pkg", n); o.exit != 0 {
			t.Errorf("dump pkg %s failed:\n%s", n, o)
		}
	}
}

// Broken packages must be rejected with the same message by every compiler.
func TestPackageErrors(t *testing.T) {
	dir, _ := filepath.Abs("testdata/badreg")
	files := corpus(t, "testdata/badreg/*.pkg")
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".pkg")
		o := agree(t, "dump", "pkg", "--registry", "file://"+dir, name)
		if (o.exit == 0) != (name == "good") {
			t.Errorf("%s: exit %d\n%s", name, o.exit, o)
		}
	}
	// fetch errors: a missing package and an invalid name
	agree(t, "dump", "pkg", "--registry", "file://"+dir, "missing")
	agree(t, "dump", "pkg", "--registry", "file://"+dir, "9bad")
	// command-line errors
	agree(t, "dump", "nope", "x")
	agree(t, "dump", "pkg")
	agree(t, "--help")
}

// The bootstrap: pkc built by the Go compiler (stage 1) builds itself
// (stage 2), which builds itself again (stage 3). All three binaries must
// be identical, byte for byte.
func TestBootstrap(t *testing.T) {
	if _, err := exec.LookPath("nasm"); err != nil {
		t.Skip("nasm not installed")
	}
	if testing.Short() {
		t.Skip("the bootstrap takes about 20 seconds")
	}
	dir := t.TempDir()
	stage := func(compiler, out string) []byte {
		t.Helper()
		o := run(t, compiler, "build", "pkc.pkg", "-o", out)
		if o.exit != 0 {
			t.Fatalf("%s build pkc.pkg failed:\n%s", compiler, o)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	s1 := stage(compilers[0].bin, filepath.Join(dir, "stage1"))
	s2 := stage(filepath.Join(dir, "stage1"), filepath.Join(dir, "stage2"))
	s3 := stage(filepath.Join(dir, "stage2"), filepath.Join(dir, "stage3"))
	if !bytes.Equal(s1, s2) {
		t.Errorf("stage 2 (pkc built by pkc) differs from stage 1 (pkc built by Go)")
	}
	if !bytes.Equal(s2, s3) {
		t.Errorf("stage 3 differs from stage 2: no fixed point")
	}
	o := run(t, filepath.Join(dir, "stage3"), "run", "../examples/fib.pkg")
	if o.stdout != "9227465\n" {
		t.Errorf("stage 3 can't run fib:\n%s", o)
	}
}

// The -vb report and tree must agree, apart from timings.
func TestReport(t *testing.T) {
	if _, err := exec.LookPath("nasm"); err != nil {
		t.Skip("nasm not installed")
	}
	out := filepath.Join(t.TempDir(), "prog")
	for _, f := range corpus(t, "../examples/*.pkg", "testdata/programs/phase2.pkg", "testdata/programs/types.pkg") {
		t.Run(filepath.Base(f), func(t *testing.T) {
			agreeIn(t, "report", unTimed, "tree", f)
			agreeIn(t, "report", unTimed, "build", "-vb", "-o", out, f)
			agreeIn(t, "report", unTimed, "asm", f, "-vb")
		})
	}
	agreeIn(t, "report", unTimed, "run", "-vb", "../examples/exitcode.pkg")
	agreeIn(t, "report", unTimed, "dump", "-vb", "tokens", "../examples/fib.pkg")
	agreeIn(t, "report", unTimed, "run", "-vb", "testdata/errors/type.pkg")
	agreeIn(t, "report", unTimed, "frobnicate", "../examples/fib.pkg")
}

// Lock files: every compiler must write the same lock, build with it, and
// reject packages that changed or are missing, with the same messages.
func TestLock(t *testing.T) {
	if _, err := exec.LookPath("nasm"); err != nil {
		t.Skip("nasm not installed")
	}
	dir := t.TempDir()
	// a copy of the registry, so that a package can change under the lock
	reg := filepath.Join(dir, "reg")
	if err := os.CopyFS(reg, os.DirFS("../registry")); err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(dir, "prog.pkg")
	src, err := os.ReadFile("../examples/fib.pkg")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prog, src, 0o644); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "prog.lock")

	// making the lock: the same file from every compiler
	var lock []byte
	for _, c := range compilers {
		o := run(t, c.bin, "lock", "--registry", "file://"+reg, prog)
		if o.exit != 0 {
			t.Fatalf("%s lock failed:\n%s", c.name, o)
		}
		b, err := os.ReadFile(lockPath)
		if err != nil {
			t.Fatal(err)
		}
		if lock != nil && !bytes.Equal(b, lock) {
			t.Fatalf("%s wrote a different lock file:\n%s", c.name, diff(string(lock), string(b)))
		}
		lock = b
	}
	agree(t, "lock", "--registry", "file://"+reg, prog)
	agreeIn(t, "report", unTimed, "lock", "-vb", "--registry", "file://"+reg, prog)

	// building with it: the lock's registry wins over PACKENGER_REGISTRY
	if o := agree(t, "run", prog); o.stdout != "9227465\n" {
		t.Fatalf("run with the lock:\n%s", o)
	}
	agreeIn(t, "report", unTimed, "run", "-vb", prog)

	// a package changed in the registry
	ifPkg := filepath.Join(reg, "std", "if.pkg")
	orig, _ := os.ReadFile(ifPkg)
	os.WriteFile(ifPkg, append(orig, "// changed\n"...), 0o644)
	if o := agree(t, "run", prog); o.exit == 0 || !strings.Contains(o.stderr, "does not match") {
		t.Fatalf("a changed package was accepted:\n%s", o)
	}
	os.WriteFile(ifPkg, orig, 0o644)

	// broken lock files
	for _, text := range []string{
		"registry file://" + reg + "\n", // std is missing
		"std 0123\n",                    // not a sha256
		"// no registry line\n",         // no registry
		"registry a\nregistry b\n",      // two registries
		"registry file://" + reg + "\nstd " + strings.Repeat("0", 64) + "\n", // wrong sum
	} {
		os.WriteFile(lockPath, []byte(text), 0o644)
		if o := agree(t, "run", prog); o.exit == 0 {
			t.Errorf("lock %q was accepted:\n%s", text, o)
		}
	}
}

// Tools: tools/treestat.pkg must build with every compiler and report the
// same on this repository's registry (through index.txt and file://, so no
// network), apart from timings.
func TestTools(t *testing.T) {
	if _, err := exec.LookPath("nasm"); err != nil {
		t.Skip("nasm not installed")
	}
	o := agreeIn(t, "report", unTimed, "run", "../tools/treestat.pkg", "--", registry, "--no-fallback")
	if o.exit != 0 || !strings.Contains(o.stdout, "PACKAGE TREE") || !strings.Contains(o.stdout, "none ✓") {
		t.Fatalf("treestat on the registry:\n%s", o)
	}
}

// Package trees: "use pkg_tree" routes packages to registries with the
// pkg_tree.pkg next to the program. Every compiler must route, report and
// lock the same way, and reject broken trees with the same messages.
func TestTree(t *testing.T) {
	if _, err := exec.LookPath("nasm"); err != nil {
		t.Skip("nasm not installed")
	}
	dir := t.TempDir()
	acme, _ := filepath.Abs("../examples/web/registry")
	prog := filepath.Join(dir, "app.pkg")
	src, err := os.ReadFile("../examples/web/app.pkg")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(prog, src, 0o644)
	tree := filepath.Join(dir, "pkg_tree.pkg")
	writeTree := func(text string) { os.WriteFile(tree, []byte(text), 0o644) }

	// a route for acme; everything else from PACKENGER_REGISTRY
	writeTree("// routes\ntree\n  acme   file://" + acme + "/\n")
	o := agree(t, "run", prog)
	if o.exit != 0 || !strings.Contains(o.stdout, "Built from your own registry") {
		t.Fatalf("run with a tree:\n%s", o)
	}
	agreeIn(t, "report", unTimed, "run", "-vb", prog)

	// "*" replaces the default registry; the longest prefix wins
	reg, _ := filepath.Abs("../registry")
	writeTree("tree\n  *  " + registry + "\n  acme  file:///nowhere\n  acme/banner  file://" + acme + "\n")
	if o := agree(t, "run", prog); o.exit == 0 || !strings.Contains(o.stderr, "file:///nowhere/acme.pkg") {
		t.Fatalf("acme should come from file:///nowhere:\n%s", o)
	}
	writeTree("tree\n  *  file://" + reg + "\n  acme  file://" + acme + "\n")
	agreeIn(t, "report", unTimed, "asm", "-vb", "-o", filepath.Join(dir, "app.s"), prog)

	// --registry replaces the whole tree
	if o := agree(t, "run", "--registry", registry, prog); o.exit == 0 || !strings.Contains(o.stderr, "not found") {
		t.Fatalf("--registry should ignore the routes:\n%s", o)
	}

	// locks record the routes, and builds use them
	lockPath := filepath.Join(dir, "app.lock")
	var lock []byte
	for _, c := range compilers {
		if o := run(t, c.bin, "lock", prog); o.exit != 0 {
			t.Fatalf("%s lock failed:\n%s", c.name, o)
		}
		b, _ := os.ReadFile(lockPath)
		if !bytes.Contains(b, []byte("\nroute acme file://"+acme+"\n")) {
			t.Fatalf("%s lock has no route:\n%s", c.name, b)
		}
		if lock != nil && !bytes.Equal(b, lock) {
			t.Fatalf("%s wrote a different lock file:\n%s", c.name, diff(string(lock), string(b)))
		}
		lock = b
	}
	agree(t, "lock", prog)
	writeTree("tree\n  acme  file:///nowhere\n")
	if o := agree(t, "run", prog); o.exit != 0 {
		t.Fatalf("the lock's routes should win over the tree:\n%s", o)
	}
	agreeIn(t, "report", unTimed, "run", "-vb", prog)
	for _, text := range []string{
		"registry file://" + reg + "\nroute acme\n",
		"registry file://" + reg + "\nroute acme a\nroute acme b\n",
		"registry file://" + reg + "\nroute 9x a\n",
	} {
		os.WriteFile(lockPath, []byte(text), 0o644)
		if o := agree(t, "run", prog); o.exit == 0 {
			t.Errorf("lock %q was accepted:\n%s", text, o)
		}
	}
	os.Remove(lockPath)

	// broken trees
	for _, text := range []string{
		"",                               // no tree section
		"  acme file:///x\n",             // a route before the section
		"tree\n  acme\n",                 // no registry
		"tree\n  acme file:///x extra\n", // too many fields
		"tree\n  9acme file:///x\n",      // not a name
		"tree\n  acme localhost:8080\n",  // not a URL
		"tree\n  acme file:///x\n  acme file:///y\n",
		"tree\n  * file:///x\n  * file:///y\n",
		"tree\n  acme file:///x\ntree\n", // two sections
		"routes\n  acme file:///x\n",     // unknown section
	} {
		writeTree(text)
		if o := agree(t, "run", prog); o.exit == 0 {
			t.Errorf("tree %q was accepted:\n%s", text, o)
		}
	}
	os.Remove(tree)
	if o := agree(t, "run", prog); o.exit == 0 || !strings.Contains(o.stderr, "pkg_tree") {
		t.Fatalf("a missing tree was accepted:\n%s", o)
	}
}

// The C backend: every compiler must generate the same C, and every program
// must behave the same as with bintables.
func TestCTables(t *testing.T) {
	for _, tool := range []string{"nasm", "cc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	for _, f := range corpus(t, "testdata/programs/*.pkg", "../examples/*.pkg", "../tests/rt/*.pkg") {
		t.Run(filepath.Base(f), func(t *testing.T) {
			if o := agree(t, "dump", "asm", "--target", "ctables", f); o.exit != 0 {
				t.Fatalf("dump asm failed:\n%s", o)
			}
			src, _ := os.ReadFile(f)
			if strings.Contains(string(src), "// output: varies") {
				return
			}
			want := agree(t, "run", f)
			if got := agree(t, "run", "--target", "ctables", f); got != want {
				t.Fatalf("ctables and bintables differ:\n%s\nbintables:\n%s\nctables:\n%s", diff(want.String(), got.String()), want, got)
			}
		})
	}
}

// pkc compiled to C by the Go compiler must build the same pkc as every
// other stage.
func TestCBootstrap(t *testing.T) {
	for _, tool := range []string{"nasm", "cc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	if testing.Short() {
		t.Skip("the C bootstrap takes about 20 seconds")
	}
	dir := t.TempDir()
	build := func(compiler, out string, args ...string) []byte {
		t.Helper()
		o := run(t, compiler, append([]string{"build", "pkc.pkg", "-o", out}, args...)...)
		if o.exit != 0 {
			t.Fatalf("%s build pkc.pkg %v failed:\n%s", compiler, args, o)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	asm := build(compilers[0].bin, filepath.Join(dir, "pkc-asm"))
	build(compilers[0].bin, filepath.Join(dir, "pkc-c"), "--target", "ctables")
	viaC := build(filepath.Join(dir, "pkc-c"), filepath.Join(dir, "stage2"))
	if !bytes.Equal(asm, viaC) {
		t.Errorf("pkc compiled to C builds a different pkc than pkc compiled to NASM")
	}
	c1 := build(filepath.Join(dir, "pkc-c"), filepath.Join(dir, "c1"), "--target", "ctables")
	c2 := build(filepath.Join(dir, "c1"), filepath.Join(dir, "c2"), "--target", "ctables")
	if !bytes.Equal(c1, c2) {
		t.Errorf("pkc compiled to C has no fixed point")
	}
}

// webSkip are programs that need what WASI doesn't have: processes, the
// environment and reading directories.
var webSkip = map[string]bool{"tools.pkg": true, "os.pkg": true, "fs.pkg": true}

// The WebAssembly backend: every compiler must generate the same module, and
// every program must behave the same as with bintables.
func TestWebTables(t *testing.T) {
	for _, tool := range []string{"nasm", "wat2wasm", "wasmtime"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	for _, f := range corpus(t, "testdata/programs/*.pkg", "../examples/*.pkg", "../tests/rt/*.pkg") {
		t.Run(filepath.Base(f), func(t *testing.T) {
			if o := agree(t, "dump", "asm", "--target", "webtables", f); o.exit != 0 {
				t.Fatalf("dump asm failed:\n%s", o)
			}
			src, _ := os.ReadFile(f)
			if webSkip[filepath.Base(f)] || strings.Contains(string(src), "// output: varies") {
				return
			}
			want := agree(t, "run", f)
			if got := agree(t, "run", "--target", "webtables", f); got != want {
				t.Fatalf("webtables and bintables differ:\n%s\nbintables:\n%s\nwebtables:\n%s", diff(want.String(), got.String()), want, got)
			}
		})
	}
}

// pkc compiled to WebAssembly, running in wasmtime, must generate the same
// module for itself as the Go compiler does.
func TestWebBootstrap(t *testing.T) {
	for _, tool := range []string{"wat2wasm", "wasmtime"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	if testing.Short() {
		t.Skip("the WebAssembly bootstrap takes a few seconds")
	}
	wasm := filepath.Join(t.TempDir(), "pkc.wasm")
	if o := run(t, compilers[0].bin, "build", "--target", "webtables", "pkc.pkg", "-o", wasm); o.exit != 0 {
		t.Fatalf("building pkc.wasm:\n%s", o)
	}
	want := run(t, compilers[0].bin, "asm", "--target", "webtables", "pkc.pkg")
	// WASI programs can't read the environment, so the registry is a flag
	got := run(t, "wasmtime", "run", "--dir=/", "--dir=.", wasm, "asm", "--target", "webtables", "pkc.pkg", "--registry", registry)
	if got.exit != 0 || got.stdout != want.stdout {
		t.Fatalf("pkc.wasm generated a different module for itself:\n%s\n%s", diff(want.stdout, got.stdout), got.stderr)
	}
}

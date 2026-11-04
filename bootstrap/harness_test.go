// Package bootstrap holds the comparison harness for self-hosting: it runs
// every compiler under test on the same inputs and requires identical
// output at every stage. See docs/self-hosting.md and docs/dumps.md.
//
// Compilers under test:
//
//   - the Go compiler, built from this repository;
//   - pkc, if the PKC environment variable names its executable. While pkc
//     is being ported, PKC_STAGES limits it to the stages it supports, as a
//     comma-separated list of pkg, tokens, ast, asm and run.
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
	var first outcome
	var firstName string
	stage := args[0]
	if stage == "dump" && len(args) > 1 {
		stage = args[1]
	}
	switch stage {
	case "tokens", "ast", "asm", "run":
	default:
		stage = "pkg" // package files and command-line handling
	}
	for _, c := range compilers {
		if c.stages != nil && !c.stages[stage] {
			continue
		}
		for i := 1; i <= runs; i++ {
			o := run(t, c.bin, args...)
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

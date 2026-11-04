package lang_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DominicThemiya/Packenger/internal/lang"
	"github.com/DominicThemiya/Packenger/internal/registry"
)

var registryDir, _ = filepath.Abs("../../registry")

func compiler(base string) *lang.Compiler {
	return &lang.Compiler{Registry: registry.New(base), Target: "bintables"}
}

// run compiles src, assembles it and returns stdout and the exit code.
func run(t *testing.T, base, src string) (string, int) {
	t.Helper()
	if _, err := exec.LookPath("nasm"); err != nil {
		t.Skip("nasm not installed")
	}
	res, err := compiler(base).Compile(context.Background(), "test.pkg", src)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "prog")
	if err := lang.Assemble(res.Asm, exe); err != nil {
		t.Fatalf("%v\n%s", err, res.Asm)
	}
	out, err := exec.Command(exe).Output()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func TestPrograms(t *testing.T) {
	cases := []struct {
		name, src, out string
		code           int
	}{
		{"arith", "print (1 + 2) * -3\nprint 7 / 2\nprint -7 % 3\nprint 10 - 2 - 3", "-9\n3\n-1\n5\n", 0},
		{"logic", "print !0 && 5 > 2 || 0\nprint 0 || 0\nprint 3 != 3\nprint 2 >= 2", "1\n0\n0\n1\n", 0},
		{"strings", "let s = \"héllo\"\nprint s\nprint \"\"", "héllo\n\n", 0},
		{"if-else", "let x = 5\nif x > 3\n  print 1\nelse\n  print 2\nend\nif x\n  print 3\nend", "1\n3\n", 0},
		{"while", "let i = 0\nlet s = 0\nwhile i < 10\n  s = s + i\n  i = i + 1\nend\nprint s", "45\n", 0},
		{"for", "let s = 0\nfor i = 1 to 100\n  s = s + i\nend\nprint s", "5050\n", 0},
		{"recursion", "fn fact(n)\n  if n <= 1\n    return 1\n  end\n  return n * fact(n - 1)\nend\nprint fact(20)", "2432902008176640000\n", 0},
		{"args-order", "fn f(a, b, c)\n  return a * 100 + b * 10 + c\nend\nprint f(1, 2, 3)", "123\n", 0},
		{"call-before-def", "print twice(21)\nfn twice(x)\n  return x * 2\nend", "42\n", 0},
		{"scopes", "let x = 1\nif 1\n  let x = 2\n  print x\nend\nprint x", "2\n1\n", 0},
		{"exit-code", "return 7", "", 7},
		{"extremes", "print 9223372036854775807\nprint -9223372036854775807 - 1", "9223372036854775807\n-9223372036854775808\n", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, code := run(t, "file://"+registryDir, "use std\n"+c.src+"\n")
			if out != c.out || code != c.code {
				t.Errorf("got %q (exit %d), want %q (exit %d)", out, code, c.out, c.code)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	cases := []struct{ src, want string }{
		{"print y", "unknown variable 'y'"},
		{"print f(1)", "unknown function f"},
		{"fn f(a)\n  return a\nend\nprint f(1, 2)", "takes 1 argument(s), got 2"},
		{"let s = \"a\"\nprint s + 1", "left side of '+' must be int, but it is str"},
		{"if 1\n  print 2", "expected 'end'"},
		{"print 1 $ 2", "unknown symbol '$'"},
		{"let x = 1\nlet x = 2", "already declared"},
		{"while 1\n  fn g()\n  end\nend", "only be used at the top level"},
	}
	for _, c := range cases {
		_, err := compiler("file://"+registryDir).Compile(context.Background(), "e.pkg", "use std\n"+c.src+"\n")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: got %v, want error containing %q", c.src, err, c.want)
		}
	}
	if _, err := compiler("file://"+registryDir).Compile(context.Background(), "e.pkg", "print 1\n"); err == nil || !strings.Contains(err.Error(), "use std") {
		t.Errorf("missing use: got %v", err)
	}
}

// New syntax is just a new package: no compiler change needed.
func TestLanguageIsExtendedByPackages(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(registryDir)); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// a front-end-only package that expands into std/if and std/not
	write("unless.pkg", "needs std/if std/not\nsyntax statement\n  unless {cond: expr int} {body: block} end\nexpand\n  if !({cond})\n    {body}\n  end\n")
	// a new operator with its own syntax package and bintables package
	write("std/max.pkg", "syntax infix 45 left\n  {left: expr int} max {right: expr int} -> int\n")
	write("bintables/max.pkg", "emit\n  @binop\n  cmp rax, rbx\n  cmovl rax, rbx\n")
	out, _ := run(t, "file://"+dir, "use std\nuse unless\nuse std/max\nunless 1 > 2\n  print 3 max 9 max 4\nend\n")
	if out != "9\n" {
		t.Errorf("got %q", out)
	}
}

func TestFetchesOverHTTPOncePerPackage(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, err := os.ReadFile(filepath.Join(registryDir, filepath.FromSlash(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	res, err := compiler(srv.URL).Compile(context.Background(), "x.pkg", "use std\nprint 1 + 2\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := len(res.Front) + len(res.Back); int(hits.Load()) != want {
		t.Errorf("%d requests for %d packages", hits.Load(), want)
	}
	if !strings.Contains(res.Asm, "add rax, 2") {
		t.Errorf("expected the simple-operand add variant:\n%s", res.Asm)
	}
	if _, err := compiler(srv.URL).Compile(context.Background(), "x.pkg", "use nope\n"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

func TestExamples(t *testing.T) {
	want := map[string]string{
		"hello.pkg":    "Hello, Packenger!\n",
		"fib.pkg":      "9227465\n",
		"primes.pkg":   "primes below 100000:\n9592\n",
		"fizzbuzz.pkg": "1\n2\nFizz\n4\nBuzz\nFizz\n7\n8\nFizz\nBuzz\n11\nFizz\n13\n14\nFizzBuzz\n",
	}
	files, _ := filepath.Glob("../../examples/*.pkg")
	for _, f := range files {
		src, _ := os.ReadFile(f)
		out, code := run(t, "file://"+registryDir, string(src))
		if w, ok := want[filepath.Base(f)]; ok && out != w {
			t.Errorf("%s: got %q", f, out)
		}
		if filepath.Base(f) == "exitcode.pkg" && code != 12 {
			t.Errorf("exitcode.pkg: exit %d, want 12", code)
		}
	}
}

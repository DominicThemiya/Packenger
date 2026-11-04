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
	if err := lang.Assemble(res.Asm, exe, nil); err != nil {
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
		{"logic", "print !false && 5 > 2 || false\nprint false || false\nprint 3 != 3\nprint 2 >= 2", "true\nfalse\nfalse\ntrue\n", 0},
		{"strings", "let s = \"héllo\"\nprint s\nprint \"\"", "héllo\n\n", 0},
		{"if-else", "let x = 5\nif x > 3\n  print 1\nelse\n  print 2\nend\nif x != 0\n  print 3\nend", "1\n3\n", 0},
		{"while", "let i = 0\nlet s = 0\nwhile i < 10\n  s = s + i\n  i = i + 1\nend\nprint s", "45\n", 0},
		{"for", "let s = 0\nfor i = 1 to 100\n  s = s + i\nend\nprint s", "5050\n", 0},
		{"recursion", "fn fact(n: int) -> int\n  if n <= 1\n    return 1\n  end\n  return n * fact(n - 1)\nend\nprint fact(20)", "2432902008176640000\n", 0},
		{"args-order", "fn f(a: int, b: int, c: int) -> int\n  return a * 100 + b * 10 + c\nend\nprint f(1, 2, 3)", "123\n", 0},
		{"call-before-def", "print twice(21)\nfn twice(x: int) -> int\n  return x * 2\nend", "42\n", 0},
		{"scopes", "let x = 1\nif true\n  let x = 2\n  print x\nend\nprint x", "2\n1\n", 0},
		{"exit-code", "return 7", "", 7},
		{"bools", "let b = 3 < 4\nprint b\nprint !b\nprint b == true\nprint 5 as bool", "true\nfalse\ntrue\ntrue\n", 0},
		{"u8", "let c = 'a'\nprint c\nprint (c as int) + 1\nprint 300 as u8\nprint 'b' > c", "97\n98\n44\ntrue\n", 0},
		{"strings-eq", "print \"ab\" == \"ab\"\nprint \"ab\" == \"abc\"\nprint \"ab\" != \"ax\"\nlet s = \"hi\"\nprint len(s)\nprint s[1] == 'i'", "true\nfalse\ntrue\n2\ntrue\n", 0},
		{"structs", "struct P\n  x: int\n  ok: bool\n  tag: u8\n  next: ptr P\nend\nlet p = new P\nprint p.x\nprint p.ok\nprint p.next == null\np.x = 7\np.ok = true\np.tag = 'z'\np.next = new P\np.next.x = p.x * 6\nprint p.next.x\nprint p.ok\nprint p.tag", "0\nfalse\ntrue\n42\ntrue\n122\n", 0},
		{"struct-later", "fn mk() -> ptr Q\n  let q = new Q\n  q.v = 5\n  return q\nend\nprint mk().v\nstruct Q\n  v: int\nend", "5\n", 0},
		{"slices", "let a = make([int], 4)\nfor i = 0 to 3\n  a[i] = i * 10\nend\nprint a[3]\nprint len(a)\nlet b = make([u8], 2)\nb[1] = 'q'\nprint b[1]\nprint b[0]\nlet nested = make([[int]], 2)\nnested[1] = a\nprint nested[1][2]", "30\n4\n113\n0\n20\n", 0},
		{"pointers", "fn bump(c: ptr int)\n  *c = *c + 1\nend\nlet n = 1\nbump(&n)\nbump(&n)\nprint n\nlet a = make([int], 2)\nlet p = &a[1]\n*p = 9\nprint a[1]", "3\n9\n", 0},
		{"void-return", "fn log(s: str)\n  if len(s) == 0\n    return\n  end\n  print s\nend\nlog(\"\")\nlog(\"x\")", "x\n", 0},
		{"mutual", "fn even(n: int) -> bool\n  if n == 0\n    return true\n  end\n  return odd(n - 1)\nend\nfn odd(n: int) -> bool\n  if n == 0\n    return false\n  end\n  return even(n - 1)\nend\nprint even(10)\nprint odd(7)", "true\ntrue\n", 0},
		{"elif", "for i = 1 to 15\n  if i % 15 == 0\n    print \"FB\"\n  elif i % 5 == 0\n    print \"B\"\n  elif i % 3 == 0\n    print \"F\"\n  end\nend", "F\nB\nF\nF\nB\nF\nFB\n", 0},
		{"break-continue", "let s = 0\nfor i = 1 to 100\n  if i % 2 == 0\n    continue\n  end\n  if i > 9\n    break\n  end\n  s = s + i\nend\nprint s\nlet j = 0\nwhile j < 5\n  if j == 2\n    continue\n  end\n  print j\nnext\n  j = j + 1\nend", "25\n0\n1\n3\n4\n", 0},
		{"nested-loops", "for i = 1 to 3\n  for j = 1 to 3\n    if j == 2\n      break\n    end\n    print i * 10 + j\n  end\nend", "11\n21\n31\n", 0},
		{"globals", "fn bump()\n  count = count + 1\nend\nglobal count: int = 5\nbump()\nbump()\nprint count\nglobal name: str = \"g\"\nprint name\nglobal p: ptr int\nprint p == null", "7\ng\ntrue\n", 0},
		{"consts", "print AREA\nconst W = 3\nconst H = W + 1\nconst AREA = W * H\nconst S = \"hi\"\nprint S\nfn f() -> int\n  return W\nend\nprint f()", "12\nhi\n3\n", 0},
		{"bits", "print 12 & 10\nprint 12 | 3\nprint 12 ^ 10\nprint 1 << 62\nprint -16 >> 2\nlet k = 3\nprint 1 << k", "8\n15\n6\n4611686018427387904\n-4\n8\n", 0},
		{"syscall", "let n = syscall(1, 1, cstr(\"raw\\n\"), 4)\nprint n\nprint len(argv)", "raw\n4\n1\n", 0},
		{"index-panic", "let a = make([int], 2)\nprint a[2]", "", 2},
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
		{"fn f(a: int) -> int\n  return a\nend\nprint f(1, 2)", "takes 1 argument(s), got 2"},
		{"fn f(a: int) -> int\n  return a\nend\nprint f(\"x\")", "argument 1 of f must be int, but it is str"},
		{"let s = \"a\"\nprint s + 1", "left side of '+' must be int, but it is str"},
		{"if true\n  print 2", "expected 'end'"},
		{"if 1\n  print 2\nend", "{cond} of if must be bool, but it is int"},
		{"print 1 $ 2", "unknown symbol '$'"},
		{"let x = 1\nlet x = 2", "already declared"},
		{"while true\n  fn g()\n  end\nend", "only be used at the top level"},
		{"let x = 1\nx = \"s\"", "{value} of assign must be int, but it is str"},
		{"let x: int = true", "{value} of let must be int, but it is bool"},
		{"fn f(n)\nend", "parameter n needs a type"},
		{"fn f() -> int\n  return \"s\"\nend", "f must return int, but this is str"},
		{"fn f()\n  return 1\nend", "f returns nothing"},
		{"fn f() -> int\n  return\nend", "f must return int"},
		{"fn f()\nend\nprint f()", "needs a value, but this has none"},
		{"let p = new Nope", "unknown type Nope"},
		{"struct P\n  x: int\nend\nlet p = new P\nprint p.y", "struct P has no field y"},
		{"let n = 3\nprint n.x", "the left side of '.' must be of the form ptr"},
		{"struct P\n  x: int\nend\nlet v: P = null", "can't hold a struct value"},
		{"let a = make([int], 3)\nprint a[\"0\"]", "{i} of index must be int, but it is str"},
		{"print 1 == true", "right side of '==' must be int, but it is bool"},
		{"let s = \"x\"\ns[0] = 'y'", "can't be assigned to"},
		{"print &3", "can't have its address taken"},
		{"break", "can only be used inside a loop"},
		{"fn f()\n  continue\nend", "can only be used inside a loop"},
		{"let x = 1\nconst C = x + 1", "must not use variables or calls"},
		{"global g: int\nglobal g: int", "g is already declared"},
		{"const C = 1\nC = 2", "can't be assigned to"},
		{"fn f()\n  global g: int\nend", "may only be used at the top level"},
		{"print syscall()", "has no 'emit' section that applies"},
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
	write("unless.pkg", "doc unless\nneeds std/if std/not\nsyntax statement\n  unless {cond: expr bool} {body: block} end\nexpand\n  if !({cond})\n    {body}\n  end\n")
	// a new operator with its own syntax package and bintables package
	write("std/max.pkg", "syntax infix 45 left\n  {left: expr int} max {right: expr int} -> int\n")
	write("bintables/max.pkg", "emit\n  @binop\n  cmp rax, rbx\n  cmovl rax, rbx\n")
	out, _ := run(t, "file://"+dir, "use std\nuse unless\nuse std/max\nunless 1 > 2\n  print 3 max 9 max 4\nend\n")
	if out != "9\n" {
		t.Errorf("got %q", out)
	}

	// private names: two expansions in one block don't collide
	write("swap.pkg", "doc swap\nneeds std\nsyntax statement\n  swap {a: var} {b: var}\nexpand\n  let {$t} = {a}\n  {a} = {b}\n  {b} = {$t}\n")
	out, _ = run(t, "file://"+dir, "use std\nuse swap\nlet x = 1\nlet y = 2\nswap x y\nswap x y\nswap x y\nprint x\nprint y\n")
	if out != "2\n1\n" {
		t.Errorf("swap: got %q", out)
	}
}

// A library package ships functions, structs, globals and constants as code.
func TestLibraryPackages(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(registryDir)); err != nil {
		t.Fatal(err)
	}
	lib := "doc A test library.\nneeds std\ncode\n  const LIB_BASE = 100\n  global lib_calls: int = 0\n\n  struct LibBox\n    v: int\n  end\n\n  fn lib_box(v: int) -> ptr LibBox\n    lib_calls = lib_calls + 1\n    let b = new LibBox\n    b.v = v + LIB_BASE\n    return b\n  end\n"
	if err := os.WriteFile(filepath.Join(dir, "mylib.pkg"), []byte(lib), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := run(t, "file://"+dir, "use std\nuse mylib\nprint lib_box(5).v\nprint lib_box(1).v\nprint lib_calls\nprint LIB_BASE\n")
	if out != "105\n101\n2\n100\n" {
		t.Errorf("got %q", out)
	}

	bad := "doc Bad.\nneeds std\ncode\n  fn ok()\n  end\n  print 1\n"
	os.WriteFile(filepath.Join(dir, "badlib.pkg"), []byte(bad), 0o644)
	_, err := compiler("file://"+dir).Compile(context.Background(), "x.pkg", "use std\nuse badlib\n")
	if err == nil || !strings.Contains(err.Error(), "badlib.pkg:6:3: library code (badlib) may only contain") {
		t.Errorf("got %v", err)
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
		"linked.pkg":   "5050\n",
		"words.pkg":    "5\n8\n",
		"upcase.pkg":   "",
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

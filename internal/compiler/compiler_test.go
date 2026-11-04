package compiler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DominicThemiya/Packenger/internal/compiler"
	"github.com/DominicThemiya/Packenger/internal/machine"
	"github.com/DominicThemiya/Packenger/internal/registry"
)

var registryDir, _ = filepath.Abs("../../registry")

func compile(t *testing.T, base, src string) *compiler.Program {
	t.Helper()
	p, err := compiler.New(registry.New(base)).CompileSource(context.Background(), "test.pkg", src)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func decode(t *testing.T, bits string) (*machine.Machine, *machine.Node) {
	t.Helper()
	n, err := machine.Decode(bits, machine.I)
	if err != nil {
		t.Fatal(err)
	}
	return &machine.Machine{MaxSteps: 1e9}, n
}

func TestEveryHostedPackageIsApplicative(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(registryDir, "*.pkg"))
	if len(files) == 0 {
		t.Fatal("no registry packages found")
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".pkg")
		p := compile(t, "file://"+registryDir, name)
		if !machine.IsApplicative(p.Bits) {
			t.Errorf("%s compiles to a non-applicative bit string", name)
		}
	}
}

func TestNumbers(t *testing.T) {
	for _, n := range []uint64{0, 1, 2, 7, 64, 255, 1000} {
		p := compile(t, "file://"+registryDir, "add 0 "+itoa(n))
		m, prog := decode(t, p.Bits)
		got, err := m.Nat(prog)
		if err != nil || got != n {
			t.Errorf("literal %d: got %d, %v", n, got, err)
		}
	}
}

func TestPrograms(t *testing.T) {
	cases := []struct{ src, kind, want string }{
		{`if true 1 2`, "nat", "1"},
		{`if (lt 3 2) "a" "b"`, "string", "b"},
		{`eq (mul 6 7) 42`, "bool", "true"},
		{`sub 3 10`, "nat", "0"},
		{`div 100 7`, "nat", "14"},
		{`mod 100 7`, "nat", "2"},
		{`show 2026`, "string", "2026"},
		{`sum (upto 10)`, "nat", "55"},
		{`length (append "ab" "cde")`, "nat", "5"},
		{"sq := \\n. mul n n\nmap sq (upto 3)", "list", "1,4,9"},
	}
	for _, c := range cases {
		p := compile(t, "file://"+registryDir, c.src)
		m, prog := decode(t, p.Bits)
		var got string
		var err error
		switch c.kind {
		case "nat":
			var n uint64
			n, err = m.Nat(prog)
			got = itoa(n)
		case "bool":
			var b bool
			b, err = m.Bool(prog)
			got = map[bool]string{true: "true", false: "false"}[b]
		case "string", "list":
			var parts []string
			err = m.List(prog, func(e *machine.Node) error {
				n, err := m.Nat(e)
				if c.kind == "string" {
					parts = append(parts, string(rune(n)))
				} else {
					parts = append(parts, itoa(n))
				}
				return err
			})
			sep := map[string]string{"string": "", "list": ","}[c.kind]
			got = strings.Join(parts, sep)
		}
		if err != nil || got != c.want {
			t.Errorf("%q: got %q, %v; want %q", c.src, got, err, c.want)
		}
	}
}

func TestExamplesCompile(t *testing.T) {
	files, _ := filepath.Glob("../../examples/*.pkg")
	for _, f := range files {
		if _, err := compiler.New(registry.New("file://"+registryDir)).CompileFile(context.Background(), f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestFetchesOverHTTPOncePerPackage(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, err := os.ReadFile(filepath.Join(registryDir, filepath.Base(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	p := compile(t, srv.URL, `if (is-zero 0) (mul 6 7) 0`)
	if int(hits.Load()) != p.Fetched {
		t.Errorf("%d requests for %d packages", hits.Load(), p.Fetched)
	}
	m, prog := decode(t, p.Bits)
	if n, _ := m.Nat(prog); n != 42 {
		t.Errorf("got %d", n)
	}
	if _, err := compiler.New(registry.New(srv.URL)).CompileSource(context.Background(), "x.pkg", "missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

func TestCycleIsRejected(t *testing.T) {
	_, err := compiler.New(registry.New("file://"+registryDir)).CompileSource(context.Background(), "c.pkg", "a := b\nb := a\na")
	if err == nil || !strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("expected cycle error, got %v", err)
	}
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

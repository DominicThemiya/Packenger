// Command packenger compiles and runs Packenger programs.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/DominicThemiya/Packenger/internal/compiler"
	"github.com/DominicThemiya/Packenger/internal/machine"
	"github.com/DominicThemiya/Packenger/internal/registry"
)

const usage = `packenger: a language where everything is a package.

Usage:
  packenger build  <file.pkg> [-o out.jot]   compile to a bit string
  packenger run    <file.pkg|file.jot>       compile (if needed) and run
  packenger tree   <file.pkg>                show the package tree down to 0 and 1
  packenger eval   '<expression>'            compile and run an inline expression

Flags:
  --registry URL   where packages are hosted (env PACKENGER_REGISTRY;
                   default ` + registry.DefaultBase + `)
  --as KIND        how to read the result: nat, bool, list, string, term
                   (default: the file's "# output:" directive, else term)
  --max-steps N    reduction step limit (default 1000000000, 0 = none)
  -o FILE          output file for build
  -v               log every package fetched
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	cmd := args[0]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	defReg := registry.DefaultBase
	if env := os.Getenv("PACKENGER_REGISTRY"); env != "" {
		defReg = env
	}
	regURL := fs.String("registry", defReg, "")
	as := fs.String("as", "", "")
	maxSteps := fs.Uint64("max-steps", 1_000_000_000, "")
	out := fs.String("o", "", "")
	verbose := fs.Bool("v", false, "")
	if err := fs.Parse(flagsFirst(args[1:], map[string]bool{"v": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%s expects exactly one argument (see packenger --help)", cmd)
	}
	target := fs.Arg(0)

	c := compiler.New(registry.New(*regURL))
	if *verbose {
		c.Log = func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }
	}
	ctx := context.Background()
	compile := func() (*compiler.Program, error) {
		if cmd == "eval" {
			return c.CompileSource(ctx, "<eval>", target)
		}
		return c.CompileFile(ctx, target)
	}

	switch cmd {
	case "build":
		p, err := compile()
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "unwrapped %d hosted packages into %d bits\n", p.Fetched, len(p.Bits))
		if *out == "" {
			fmt.Println(p.Bits)
			return nil
		}
		return os.WriteFile(*out, []byte(p.Bits+"\n"), 0o644)
	case "tree":
		p, err := compile()
		if err != nil {
			return err
		}
		p.Tree(os.Stdout)
		return nil
	case "run", "eval":
		var bits, kind string
		if cmd == "run" && strings.HasSuffix(target, ".jot") {
			b, err := os.ReadFile(target)
			if err != nil {
				return err
			}
			bits = string(b)
		} else {
			p, err := compile()
			if err != nil {
				return err
			}
			bits, kind = p.Bits, p.Output
		}
		if *as != "" {
			kind = *as
		}
		return execute(os.Stdout, bits, kind, *maxSteps, *verbose)
	}
	return fmt.Errorf("unknown command %q (see packenger --help)", cmd)
}

func execute(w io.Writer, bits, kind string, maxSteps uint64, verbose bool) error {
	prog, err := machine.Decode(strings.TrimSpace(bits), machine.I)
	if err != nil {
		return err
	}
	m := &machine.Machine{MaxSteps: maxSteps}
	if verbose {
		defer func() { fmt.Fprintf(os.Stderr, "%d reduction steps\n", m.Steps) }()
	}
	switch kind {
	case "nat":
		n, err := m.Nat(prog)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, n)
	case "bool":
		b, err := m.Bool(prog)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, b)
	case "list":
		var items []string
		err := m.List(prog, func(e *machine.Node) error {
			n, err := m.Nat(e)
			items = append(items, fmt.Sprint(n))
			return err
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "[%s]\n", strings.Join(items, ", "))
	case "string":
		var sb strings.Builder
		err := m.List(prog, func(e *machine.Node) error {
			n, err := m.Nat(e)
			sb.WriteRune(rune(n))
			return err
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(w, sb.String())
	case "", "term":
		nf, err := m.Normalize(prog)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, m.Show(nf, 200))
	default:
		return fmt.Errorf("unknown output kind %q (nat, bool, list, string, term)", kind)
	}
	return nil
}

// flagsFirst moves flags ahead of positional arguments so that
// "packenger run file.pkg --as nat" works with the standard flag package.
func flagsFirst(args []string, boolFlags map[string]bool) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") && !boolFlags[name] && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

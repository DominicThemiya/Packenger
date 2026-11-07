// Command packenger compiles Packenger programs to native executables.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/DominicThemiya/Packenger/internal/lang"
	"github.com/DominicThemiya/Packenger/internal/pkgfile"
	"github.com/DominicThemiya/Packenger/internal/registry"
	"github.com/DominicThemiya/Packenger/internal/ui"
)

const usage = `packenger: a language where everything is a package.

Usage:
  packenger build <file.pkg> [-o exe]   compile to a native executable
  packenger run   <file.pkg> [-- args]  compile and run
  packenger asm   <file.pkg> [-o file]  print the generated NASM
  packenger tree  <file.pkg>            show which packages built the program
  packenger lock  <file.pkg>            pin the exact version of every package
                                        the program is built from, in
                                        <file>.lock (see docs/publishing.md)
  packenger dump  tokens|ast|asm <file.pkg>
                                        print a compiler stage in a stable text
                                        format (see docs/dumps.md)
  packenger dump  pkg <package>         print a registry package as parsed

Flags:
  --registry URL   where packages are hosted (env PACKENGER_REGISTRY;
                   default ` + registry.DefaultBase + `)
  --target TRUCK   backend truck that translates packages to assembly
                   (default bintables)
  -o FILE          output file
  -vb              verbose build: every package downloaded (and why), the
                   grammar, parse results, chosen backend variants, and the
                   nasm/ld steps, all with sizes and timings (-v is an alias)

Requires nasm and ld for build and run.
`

func main() {
	var src string
	code, err := run(os.Args[1:], &src)
	if err != nil {
		ui.Error(os.Stderr, ui.Colors(os.Stderr), err, src)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(args []string, srcOut *string) (int, error) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return 0, nil
	}
	cmd := args[0]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	defReg := registry.DefaultBase
	if env := os.Getenv("PACKENGER_REGISTRY"); env != "" {
		defReg = env
	}
	regURL := fs.String("registry", defReg, "")
	target := fs.String("target", "bintables", "")
	out := fs.String("o", "", "")
	verbose := fs.Bool("vb", false, "")
	fs.BoolVar(verbose, "v", false, "")
	rest := args[1:]
	var progArgs []string
	if cmd == "run" {
		// arguments after "--" go to the program
		for i, a := range rest {
			if a == "--" {
				progArgs, rest = rest[i+1:], rest[:i]
				break
			}
		}
	}
	if err := fs.Parse(flagsFirst(rest, map[string]bool{"v": true, "vb": true})); err != nil {
		return 1, err
	}
	stage := ""
	if cmd == "dump" {
		if fs.NArg() != 2 {
			return 1, fmt.Errorf("usage: packenger dump tokens|ast|asm <file.pkg>, or packenger dump pkg <package>")
		}
		stage = fs.Arg(0)
		if stage != "tokens" && stage != "ast" && stage != "asm" && stage != "pkg" {
			return 1, fmt.Errorf("unknown dump %q (tokens, ast, asm or pkg)", stage)
		}
	} else if fs.NArg() != 1 {
		return 1, fmt.Errorf("%s expects one source file (see packenger --help)", cmd)
	}
	src := fs.Arg(fs.NArg() - 1)
	*srcOut = src

	exe := *out
	if cmd == "build" && exe == "" {
		exe = strings.TrimSuffix(filepath.Base(src), ".pkg")
	}
	explicitReg := false
	fs.Visit(func(f *flag.Flag) { explicitReg = explicitReg || f.Name == "registry" })

	// "use pkg_tree" routes packages to registries with pkg_tree.pkg next to
	// the source, unless --registry is given.
	var routes []registry.Route
	if b, err := os.ReadFile(src); err == nil && !(cmd == "dump" && stage == "pkg") && lang.UsesTree(string(b)) {
		tree, err := registry.ReadTree(src)
		if err != nil {
			return 1, err
		}
		if !explicitReg {
			routes = tree.Routes
			if tree.Default != "" {
				*regURL = tree.Default
			}
		}
	}

	// A lock file next to the source pins the registries (unless --registry
	// is given) and the version of every package. "lock" itself ignores it
	// and pins the registries as they are now.
	var lock *registry.Lock
	var sums map[string]string
	lockPath := registry.LockPath(src)
	switch {
	case cmd == "lock":
		r := registry.New(*regURL)
		pinned, _, err := r.Pin(context.Background())
		if err != nil {
			return 1, err
		}
		*regURL = pinned
		for i, rt := range routes {
			if routes[i].Base, _, err = r.PinURL(context.Background(), rt.Base); err != nil {
				return 1, err
			}
		}
		sums = map[string]string{}
	case !(cmd == "dump" && stage == "pkg"):
		l, err := registry.ReadLock(lockPath, src)
		if err != nil {
			return 1, err
		}
		if l != nil && !explicitReg {
			*regURL = l.Registry
			routes = l.Routes
		}
		lock = l
	}
	reg := registry.New(*regURL)
	reg.Routes = routes
	c := &lang.Compiler{Registry: reg, Target: *target, Lock: lock, Sums: sums}
	var rep *ui.Reporter
	if *verbose {
		rep = ui.NewReporter(os.Stderr, ui.Colors(os.Stderr))
		rep.Header(cmd, src, exe, *regURL, *target)
		for _, rt := range routes {
			rep.Route(rt.Prefix, rt.Base)
		}
		if lock != nil {
			rep.Lock(lockPath, len(lock.Sums))
		}
		c.Report = rep
	}
	done := func(note string) {
		if rep != nil {
			rep.Done(note)
		}
	}
	if cmd == "dump" {
		return dump(c, stage, src)
	}
	res, err := c.CompileFile(context.Background(), src)
	if err != nil {
		return 1, err
	}

	switch cmd {
	case "asm":
		done("")
		if *out == "" {
			fmt.Print(res.Asm)
			return 0, nil
		}
		return 0, os.WriteFile(*out, []byte(res.Asm), 0o644)
	case "tree":
		done("")
		res.Tree(os.Stdout, *target)
		return 0, nil
	case "lock":
		text := registry.FormatLock(filepath.Base(src), *regURL, routes, sums)
		if err := os.WriteFile(lockPath, []byte(text), 0o644); err != nil {
			return 1, err
		}
		done("")
		var shown [][2]string
		for _, rt := range routes {
			shown = append(shown, [2]string{rt.Prefix, rt.Base})
		}
		ui.Locked(os.Stdout, ui.Colors(os.Stdout), lockPath, len(sums), *regURL, shown)
		return 0, nil
	case "build":
		if err := lang.Assemble(res.Asm, exe, res.Back["toolchain"], c.Report); err != nil {
			return 1, err
		}
		done("")
		return 0, nil
	case "run":
		dir, err := os.MkdirTemp("", "packenger-run-*")
		if err != nil {
			return 1, err
		}
		defer os.RemoveAll(dir)
		exe = filepath.Join(dir, "prog")
		if err := lang.Assemble(res.Asm, exe, res.Back["toolchain"], c.Report); err != nil {
			return 1, err
		}
		done("running")
		// some targets need a runtime to run their output
		runner := lang.RunCommand(res.Back["toolchain"], exe)
		if runner == nil {
			runner = []string{exe}
		} else if _, err := exec.LookPath(runner[0]); err != nil {
			return 1, fmt.Errorf("%s is not installed (target %s runs programs with it)", runner[0], *target)
		}
		p := exec.Command(runner[0], append(runner[1:], progArgs...)...)
		p.Stdin, p.Stdout, p.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := p.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) && ee.Exited() {
				return ee.ExitCode(), nil
			}
			return 1, err
		}
		return 0, nil
	}
	return 1, fmt.Errorf("unknown command %q (see packenger --help)", cmd)
}

func dump(c *lang.Compiler, stage, target string) (int, error) {
	ctx := context.Background()
	if stage == "pkg" {
		src, origin, err := c.Registry.Fetch(ctx, target)
		if err != nil {
			return 1, err
		}
		f, err := pkgfile.Parse(target, origin, src)
		if err != nil {
			return 1, err
		}
		fmt.Print(pkgfile.Dump(f))
		return 0, nil
	}
	src, err := os.ReadFile(target)
	if err != nil {
		return 1, err
	}
	name := filepath.Base(target)
	if stage == "tokens" {
		toks, _, _, _, err := c.Lex(ctx, name, string(src))
		if err != nil {
			return 1, err
		}
		fmt.Print(lang.DumpTokens(toks))
		return 0, nil
	}
	res, err := c.Compile(ctx, name, string(src))
	if err != nil {
		return 1, err
	}
	if stage == "ast" {
		fmt.Print(lang.DumpAST(res.Program))
	} else {
		fmt.Print(res.Asm)
	}
	return 0, nil
}

// flagsFirst moves flags ahead of positional arguments so that
// "packenger build file.pkg -o exe" works with the standard flag package.
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

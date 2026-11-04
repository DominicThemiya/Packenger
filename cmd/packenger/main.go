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
	"github.com/DominicThemiya/Packenger/internal/registry"
	"github.com/DominicThemiya/Packenger/internal/ui"
)

const usage = `packenger: a language where everything is a package.

Usage:
  packenger build <file.pkg> [-o exe]   compile to a native executable
  packenger run   <file.pkg> [-- args]  compile and run
  packenger asm   <file.pkg> [-o file]  print the generated NASM
  packenger tree  <file.pkg>            show which packages built the program

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
	if fs.NArg() != 1 {
		return 1, fmt.Errorf("%s expects one source file (see packenger --help)", cmd)
	}
	src := fs.Arg(0)
	*srcOut = src

	exe := *out
	if cmd == "build" && exe == "" {
		exe = strings.TrimSuffix(filepath.Base(src), ".pkg")
	}
	c := &lang.Compiler{Registry: registry.New(*regURL), Target: *target}
	var rep *ui.Reporter
	if *verbose {
		rep = ui.NewReporter(os.Stderr, ui.Colors(os.Stderr))
		rep.Header(cmd, src, exe, *regURL, *target)
		c.Report = rep
	}
	done := func(note string) {
		if rep != nil {
			rep.Done(note)
		}
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
	case "build":
		if err := lang.Assemble(res.Asm, exe, c.Report); err != nil {
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
		if err := lang.Assemble(res.Asm, exe, c.Report); err != nil {
			return 1, err
		}
		done("running")
		p := exec.Command(exe, progArgs...)
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

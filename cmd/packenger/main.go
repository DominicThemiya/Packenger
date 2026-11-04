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
)

const usage = `packenger: a language where everything is a package.

Usage:
  packenger build <file.pkg> [-o exe]   compile to a native executable
  packenger run   <file.pkg> [args]     compile and run
  packenger asm   <file.pkg> [-o file]  print the generated NASM
  packenger tree  <file.pkg>            show which packages built the program

Flags:
  --registry URL   where packages are hosted (env PACKENGER_REGISTRY;
                   default ` + registry.DefaultBase + `)
  --target TRUCK   backend truck that translates packages to assembly
                   (default bintables)
  -o FILE          output file
  -v               log every package fetched

Requires nasm and ld for build and run.
`

func main() {
	code, err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(args []string) (int, error) {
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
	verbose := fs.Bool("v", false, "")
	rest := args[1:]
	var progArgs []string
	if cmd == "run" {
		// everything after the source file goes to the program
		for i, a := range rest {
			if strings.HasSuffix(a, ".pkg") {
				progArgs, rest = rest[i+1:], rest[:i+1]
				break
			}
		}
	}
	if err := fs.Parse(flagsFirst(rest, map[string]bool{"v": true})); err != nil {
		return 1, err
	}
	if fs.NArg() != 1 {
		return 1, fmt.Errorf("%s expects one source file (see packenger --help)", cmd)
	}
	src := fs.Arg(0)

	c := &lang.Compiler{Registry: registry.New(*regURL), Target: *target}
	if *verbose {
		c.Log = func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }
	}
	res, err := c.CompileFile(context.Background(), src)
	if err != nil {
		return 1, err
	}

	switch cmd {
	case "asm":
		if *out == "" {
			fmt.Print(res.Asm)
			return 0, nil
		}
		return 0, os.WriteFile(*out, []byte(res.Asm), 0o644)
	case "tree":
		res.Tree(os.Stdout, *target)
		return 0, nil
	case "build":
		exe := *out
		if exe == "" {
			exe = strings.TrimSuffix(filepath.Base(src), ".pkg")
		}
		return 0, lang.Assemble(res.Asm, exe)
	case "run":
		dir, err := os.MkdirTemp("", "packenger-run-*")
		if err != nil {
			return 1, err
		}
		defer os.RemoveAll(dir)
		exe := filepath.Join(dir, "prog")
		if err := lang.Assemble(res.Asm, exe); err != nil {
			return 1, err
		}
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

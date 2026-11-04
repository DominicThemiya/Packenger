# Packenger

A compiled, statically typed programming language where **everything is a
package**, even the syntax. `if`, `while`, `+`, `print` and `fn` are not built into the
compiler. Each is a package that declares its own syntax, inputs and
output. Packages are downloaded from a hosted registry at compile time, and
the **`bintables`** truck translates them to NASM, which becomes a native
x86-64 Linux executable.

```
use std

fn fib(n: int) -> int
  if n < 2
    return n
  end
  return fib(n - 1) + fib(n - 2)
end

print fib(35)
```

```sh
go build -o bin/packenger ./cmd/packenger
./bin/packenger run examples/fib.pkg        # 9227465
./bin/packenger build examples/fib.pkg -vb  # show every step
```

fib(35) runs in 0.15 s, against 0.12 s for `gcc -O0`, 0.035 s for
`gcc -O2` and 2.1 s for Python. The executables are about 5 KB and don't
use libc.

## How it fits together

```
your program ──use std──▶ std truck          syntax packages: what the language looks like
                              │ parse
                              ▼
                          bintables truck    backend packages: NASM for each construct
                              │ nasm + ld
                              ▼
                          native executable
```

A syntax package (`registry/std/if.pkg`):

```
doc Conditional: if cond ... [else ...] end
syntax statement
  if {cond: expr bool} {then: block} [else {else: block}] end
```

Its backend package (`registry/bintables/if.pkg`) is a NASM template. The
language's types (`int`, `u8`, `bool`, `str`, pointers, slices and structs)
are defined by `std` packages too. Packages can also be built from other packages; `std/for` expands into
`let`, `while`, `<=`, `=` and `+`.

## Documentation

| | |
|---|---|
| [Getting started](docs/getting-started.md) | install, build, first program |
| [The std language](docs/language.md) | writing programs |
| [Packages and trucks](docs/packages.md) | the package format: syntax, expansion, NASM templates |
| [Publishing packages](docs/publishing.md) | the registry and keeping docs current |
| [Command line](docs/cli.md) | commands, flags and the `-vb` report |
| [How the compiler works](docs/architecture.md) | pipeline and code map |
| [Package reference](docs/registry/README.md) | every truck and package, generated from the registry |

## Requirements

Go to build the compiler, `nasm` and `ld` to assemble programs, and x86-64
Linux.

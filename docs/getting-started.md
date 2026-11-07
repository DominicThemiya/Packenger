# Getting started

## Requirements

- Go 1.23 or newer, to build the compiler
- `nasm` and `ld` (from binutils), to assemble and link programs
- x86-64 Linux, the platform the `bintables` backend targets
- optionally `gcc` or `clang` as `cc`, for the C backend (`--target ctables`)
- optionally `wat2wasm` (from wabt) and `wasmtime`, for the WebAssembly
  backend (`--target webtables`)
- network access to the registry (or a local copy, see below)

```sh
sudo pacman -S go nasm binutils      # Arch
sudo apt install golang nasm binutils  # Debian / Ubuntu
```

## Build the compiler

```sh
git clone https://github.com/DominicThemiya/Packenger
cd Packenger
go build -o bin/packenger ./cmd/packenger
```

## Your first program

Create `hello.pkg`:

```
use std

print "Hello, Packenger!"

for i = 1 to 3
  print i * i
end
```

Then:

```sh
./bin/packenger run hello.pkg                # compile and run
./bin/packenger build hello.pkg -o hello     # write an executable
./hello
```

The first line, `use std`, matters: a file without `use` has no syntax at
all. `std` is the truck (collection of packages) that defines `print`,
`for`, `+` and the rest of the language; see [the std language](language.md).

## What happened

```sh
./bin/packenger build hello.pkg -vb
```

`-vb` shows each step: the `std` packages downloaded from the registry, the
grammar built from them, the parse, the `bintables` packages downloaded to
translate it, the NASM variants chosen, and the `nasm`/`ld` steps. To see
just the package tree, or the generated assembly:

```sh
./bin/packenger tree hello.pkg
./bin/packenger asm hello.pkg
```

## Examples

The [`examples/`](../examples) folder has:

| file | shows |
|---|---|
| `hello.pkg` | strings |
| `fib.pkg` | recursion; a speed benchmark |
| `fizzbuzz.pkg` | `for`, `if`/`elif`/`else`, `%` |
| `primes.pkg` | functions, `while`, early `return`, `bool` |
| `linked.pkg` | structs, pointers, `new`, `null` |
| `words.pkg` | strings, `u8`, slices, `make`, `len` |
| `upcase.pkg` | `argv`, system calls, constants, `elif`, `break`: copies a file in upper case |
| `tools.pkg` | the `rt` truck: runs `nasm` and `curl --parallel` and times them |
| `exitcode.pkg` | the top-level `return` becomes the exit code |
| `mandelbrot.pkg` | fixed-point arithmetic, `argv`, `rt/str`'s string builder |
| `stats.pkg` | [stdx](registry/stdx.md) compound assignment: `+=`, `-=`, `*=`, `/=`, `%=` on variables, fields, elements and `*p` |
| `letters.pkg` | stdx `for x in xs` over a string's bytes and a slice, with `break`: a letter histogram |
| `roman.pkg` | stdx `match`/`case` on bytes, numbers and strings: Roman numerals |
| `bsearch.pkg` | stdx `assert`: binary search with its invariants checked; `-- unsorted` makes one fail |
| `web/` | a package registry server written in Packenger, and a program that gets packages from it |

### Your own registry

[`examples/web`](../examples/web) is a package registry server written in
Packenger, [`server.pkg`](../examples/web/server.pkg). It serves a folder
of package files over HTTP, like the universal registry on GitHub, with a
page listing every package at `/`. [`app.pkg`](../examples/web/app.pkg)
gets its `acme` packages from it and everything else from the universal
registry, because of the [package tree](publishing.md#package-trees) next
to it:

```sh
packenger run examples/web/server.pkg -- examples/web/registry   # http://localhost:6464
packenger run examples/web/app.pkg                               # in another terminal
```

## Working offline

Packages are downloaded from GitHub on every compile. To use the copy in
your checkout instead, which you'll also want while editing packages:

```sh
PACKENGER_REGISTRY=file://$PWD/registry ./bin/packenger run examples/fib.pkg
```

## The self-hosted compiler

`pkc` is the same compiler written in Packenger. Build it with the Go
compiler, then use it exactly like `packenger`:

```sh
./bin/packenger build bootstrap/pkc.pkg -o bin/pkc
./bin/pkc run examples/fib.pkg
./bin/pkc build bootstrap/pkc.pkg -o pkc2     # it can build itself
```

## Running the tests

```sh
go test ./...
```

The tests compile and run real programs, so they need `nasm` and `ld`.
`go test ./bootstrap/` also builds `pkc` three times over and checks the
binaries are identical (about 20 seconds; `go test -short` skips it). To
compare `pkc` with the Go compiler on everything:

```sh
PKC=$PWD/bin/pkc go test ./bootstrap/
``` The
`rt` truck's own tests are Packenger programs in [`tests/rt`](../tests/rt);
run one with `packenger run tests/rt/str.pkg`.

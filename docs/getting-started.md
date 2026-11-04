# Getting started

## Requirements

- Go 1.23 or newer, to build the compiler
- `nasm` and `ld` (from binutils), to assemble and link programs
- x86-64 Linux, the platform the `bintables` backend targets
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
| `fizzbuzz.pkg` | `for`, nested `if`/`else`, `%` |
| `primes.pkg` | functions, `while`, early `return` |
| `exitcode.pkg` | the top-level `return` becomes the exit code |

## Working offline

Packages are downloaded from GitHub on every compile. To use the copy in
your checkout instead, which you'll also want while editing packages:

```sh
PACKENGER_REGISTRY=file://$PWD/registry ./bin/packenger run examples/fib.pkg
```

## Running the tests

```sh
go test ./...
```

The tests compile and run real programs, so they need `nasm` and `ld`.

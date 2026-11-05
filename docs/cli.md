# Command line

```
packenger build <file.pkg> [-o exe]     compile to a native executable
packenger run   <file.pkg> [-- args]    compile and run
packenger asm   <file.pkg> [-o file]    print the generated NASM
packenger tree  <file.pkg>              show which packages built the program
packenger lock  <file.pkg>              pin every package version in <file>.lock
packenger dump  tokens|ast|asm <file>   print a compiler stage as stable text
packenger dump  pkg <package>           print a registry package as parsed
```

The `dump` formats are specified in [dump formats](dumps.md).

Flags can go before or after the file name.

| flag | meaning |
|---|---|
| `--registry URL` | where packages are hosted. Default: the registry in the program's [lock file](publishing.md#lock-files) if it has one, else the env var `PACKENGER_REGISTRY`, else `https://raw.githubusercontent.com/DominicThemiya/Packenger/main/registry`. `file://` URLs work too. |
| `--target TRUCK` | backend truck that translates the program (default `bintables`, NASM; `ctables` generates C and builds it with `cc -O2`) |
| `-o FILE` | output file for `build` (default: the source name without `.pkg`) and `asm` (default: stdout) |
| `-vb` | verbose build report on stderr (`-v` does the same) |

`run` passes arguments after `--` to the program, and exits with the
program's exit code.

`lock` compiles the program and writes `<file>.lock` next to it, pinning
the registry and the SHA-256 of every package; every later command on that
file uses it. See [lock files](publishing.md#lock-files).

## `-vb`: the build report

```
$ packenger build examples/fib.pkg -vb

  packenger build examples/fib.pkg → fib
  registry  raw.githubusercontent.com/DominicThemiya/Packenger/main/registry
  target    bintables
  uses      std

  1 syntax      front-end packages that define the language
     round 1 · 1 download
     ↓ std                    245 B   238 ms  ████████  used by fib.pkg
     round 2 · 25 downloads in parallel
     ↓ std/fn                 148 B    85 ms  ██████▏   member of truck std
     ...
     ✓ 26 packages · 2.8 KB · 365 ms

  2 grammar     assembled from those packages
     25 syntax packages → 10 forms · 13 infix operators · 2 prefix operators
     keywords  else end fn for if let print return to while
     symbols   ! != % && ( ) * + - / < <= = == > >= ||

  3 parse       your program, read with that grammar
     fib.pkg      40 tokens · 32 nodes · 1 top-level statement · 292 µs
     fn fib       1 param · 0 locals · 0 B frame

  4 bintables   backend packages that translate to NASM
     ...
     ↓ bintables/binop        136 B    74 ms  ████████  included by bintables/add

  5 emit        packages rendered to NASM
     98 lines of NASM · 722 µs
     bintables/if          ×1 when cond has jfalse
     bintables/lt          ×1 jfalse when right simple
     ...

  6 assemble    the target's toolchain builds the executable
     ⚙ nasm   13 ms
     ⚙ ld     10 ms
     ✓ fib 5.1 KB executable

  ✓ done in 702 ms
```

| phase | shows |
|---|---|
| syntax | each front-end package downloaded, in parallel rounds: size, time, a bar comparing its time to the slowest in that round, and why it was needed (`used by`, `member of truck`, `needed by`) |
| grammar | what the loaded packages define: forms, operators, keywords, symbols |
| parse | token and node counts, and each function's parameters, locals and frame size |
| bintables | each backend package downloaded, and why (`translates <kind>`, `included by`) |
| emit | which `emit` variant of each backend package was used, and how often |
| assemble | the commands of the target's `toolchain` package (`nasm` and `ld`, or `cc`) and the executable's size |

Colors are used when stderr is a terminal and `NO_COLOR` isn't set. The
report goes to stderr, so `asm` and `tree` output can still be piped.

## Errors

Errors are shown with the source line and a caret where possible, and with
a hint for common problems such as no network, an unknown package, or a
missing `nasm`:

```
  ✗ error can't download package "std": cannot resolve raw.githubusercontent.com (offline, or a DNS problem)

  hint check your internet connection, or build from a local copy of the registry:
       PACKENGER_REGISTRY=file://$PWD/registry packenger ...
```

## Other tools

```sh
go run ./cmd/pkgdoc       # regenerate docs/registry from the package files
go test ./bootstrap/      # comparison harness (PKC=/path/to/pkc to add pkc)
```

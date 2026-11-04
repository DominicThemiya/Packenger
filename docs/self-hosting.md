# Self-hosting plan

Goal: a Packenger compiler written in Packenger (**`pkc`**) that compiles
itself, developed alongside the Go compiler, with both producing identical
output.

Status: **phases 0 and 1 done** (parity infrastructure, type system).
Phases 2–5 are planned.

## Where we start

The Go compiler core is about 2,500 lines: lexer, backtracking pattern
parser, `expand`, template renderer, package fetching and driver. It leans
on string-keyed maps (58 of them), slices, structs, regular expressions in
the renderer, HTTP, files and running `nasm`/`ld`.

Today's Packenger has only `int` and `str`, no memory access, no arrays or
structs, no file or process access, no program arguments, no variables
shared between functions, and no way for a package to ship library
functions. Most of the work is growing the language until a compiler can be
written in it.

**Guiding rule:** put every new feature in packages, and keep core changes
(which must be made in both compilers) to a minimum.

## Decisions

| # | decision | consequence |
|---|---|---|
| 1 | **Download with `curl`.** | `pkc` runs `curl` the way both compilers run `nasm`/`ld`. Parallel rounds use `curl --parallel` with one output file per package. No HTTP or TLS code in Packenger. |
| 2 | **The language is typed before bootstrapping.** | The type system comes first (phase 1), and `pkc` is written in typed Packenger from the start. |
| 3 | **Compiler sources are packages in the registry.** | `pkc` is a truck (`pkc/lexer`, `pkc/parser`, ...), fetched like `std`. Development uses a `file://` registry. |
| 4 | **`-vb` and `tree` are in `pkc` version 1; `pkgdoc` comes later.** | The runtime needs terminal detection, ANSI colors, clocks, sizes and parallel downloads. `pkgdoc` stays in Go for now. |
| 5 | **Keep the Go compiler and develop both together.** | Go remains stage 0 and the reference. Every core change lands in both, and CI checks they agree. |

Note on 1: the Go compiler currently downloads with Go's `net/http`. It can
keep doing so, since how a package is downloaded doesn't affect the
generated code. Errors (for example, no network) must be reported with the
same wording by both.

## Phase 0: parity infrastructure ✓

Done. See [dump formats](dumps.md) for the formats, node numbering rules
and the harness.

Built first, because both compilers will change together from here on.

- **Deterministic node numbering.** Label numbers currently come from a
  counter that failed parse attempts also advance. Assign them in a single
  pass after parsing, so `pkc` can reproduce them exactly.
- **Deterministic iteration.** Sort wherever the Go compiler iterates a map
  in a way that affects output.
- **Dump formats**, stable text output of each stage:
  `packenger dump tokens|ast|asm <file>`. These are what the two compilers
  are compared on.
- **A comparison harness** (`bootstrap/` tests). It runs both compilers on
  every example and test program and diffs tokens, AST, NASM, exit codes
  and error messages. It runs in CI once `pkc` exists.

**Done when:** the Go compiler's dumps are deterministic across runs, and
the harness runs (comparing Go with itself).

## Phase 1: the type system ✓

Done. See [the std language](language.md#types) and
[packages and trucks](packages.md#type-constraints-and-results). Two
changes from the plan below, made during implementation:

- **Every value is one machine word.** `str` is a pointer to
  length-prefixed bytes, `[T]` a pointer to a heap header
  `{data, len, cap}`, and structs are only used through `ptr Name`. This
  keeps "every result is in `rax`" true and the backend simple; sizes only
  matter for fields and slice elements (1 or 8 bytes).
- **Two-pass parsing** makes functions and structs usable before their
  definitions, including mutual recursion.

The original plan:

The compiler needs structs, pointers, arrays, bytes and booleans. The aim is
C-level types with safer defaults, defined in packages wherever possible.

### Types

| type | meaning | size |
|---|---|---|
| `int` | 64-bit signed | 8 |
| `u8` | byte | 1 |
| `bool` | `true` / `false` | 1 |
| `str` | immutable string (pointer + length) | 16 |
| `ptr T` | pointer to `T` | 8 |
| `[T]` | slice: pointer, length, capacity | 24 |
| `struct Name` | named record | sum of its fields, aligned |

- **Primitive types are declared by packages** (a new `type` section: name,
  size, signedness), so a truck can add types. Only the constructors
  `ptr`, `[ ]` and `struct` are core.
- **Declarations:** `fn f(a: int, s: str) -> int`, `let x: int = 0` (the
  annotation is optional if it can be inferred), and
  `struct Node ... end` with `name: type` fields.
- **No user generics in version 1.** Containers in the runtime store
  64-bit words; typed code converts with an explicit `x as T`. Typed
  wrapper packages can hide the conversions.

### Core changes

1. **Type expressions** in patterns and declarations, e.g. `{p: expr ptr T}`.
2. **Type variables in slot constraints**, so packages can state rules:
   `{a: expr T} == {b: expr T} -> bool`, or `{p: expr ptr T}` with
   `-> T` for loading through a pointer.
3. **`postfix` syntax kind**, for field access `n.kind`, indexing `a[i]`
   and calls.
4. **Assignments are type-checked** (fixing the current gap), and
   function parameters and results are typed.
5. **Backend access to types:** `{slot.size}`, and conditions such as
   `when value u8`, so `bintables` picks `movzx` vs `mov`, and `cmp`
   vs a byte compare.

**Done when:** programs using structs, pointers, slices, `u8` and `bool`
compile and pass tests, and type errors point at the source like other
errors.

## Phase 2: other language features

### Core changes (in both compilers)

1. **Library packages:** a `code` section of Packenger source (functions,
   structs) merged into the program. Its names get a prefix (`rt/str.len`).
2. **Globals:** `global x: int = 0`, visible inside functions and stored in
   `.data`.
3. **Loop context**, so `break`/`continue` can reach the enclosing loop's
   labels (`{loop.L.end}`; loops are marked in their syntax header).
4. **Repeated pattern groups** `[ ... ]*`, for `if ... elif ... else ... end`.
5. **Private names in `expand`:** unique per-use names, so two `for` loops
   in one block don't collide.
6. **Constants:** `const MAX = 64`, inlined at compile time.

### New packages

| package | provides |
|---|---|
| `std/syscall` + `bintables/syscall` | `syscall(n, a, ...)`: access to every OS service |
| `std/break`, `std/continue`, `std/elif` | control flow |
| `std/band` … `std/shr` | `&`, `\|`, `^`, `<<`, `>>` |
| `bintables/program` changes | keep `argc`/`argv` for the runtime |

**Done when:** a Packenger program can read the file named in its
arguments, transform it and write it out, using only packages plus the
changes above.

## Phase 3: runtime libraries (`rt` truck, written in Packenger)

| package | provides |
|---|---|
| `rt/mem` | bump allocator on `mmap` (no freeing, which suits a compiler) |
| `rt/vec` | growable arrays |
| `rt/str` | length, compare, slice, string builder, int ↔ string, prefix tests |
| `rt/map` | hash map from `str` to a 64-bit word (replaces the Go maps) |
| `rt/io` | read and write whole files; buffered stdout/stderr |
| `rt/os` | arguments, environment, exit, and running a program and waiting for it (`fork` + `execve` + `wait4`) |
| `rt/time` | monotonic clock (`clock_gettime`), for `-vb` timings |
| `rt/term` | is stderr a terminal (`ioctl`), `NO_COLOR`, ANSI styles, padding |

**Done when:** every package has tests written in Packenger, and a small
program can run `curl --parallel` and `nasm` and report their timings.

## Phase 4: the compiler in Packenger (`pkc` truck)

Ported module by module, each checked against the Go compiler with the
phase 0 harness before moving on:

| package | Go source | must match |
|---|---|---|
| `pkc/fetch` | `registry.go` | same package bytes; same errors |
| `pkc/pkgfile` | `pkgfile.go` | same parsed structure for every registry package |
| `pkc/lexer` | `lexer.go` | `dump tokens` |
| `pkc/parser` | `parser.go` | `dump ast` |
| `pkc/codegen` | `codegen.go` | `dump asm`, byte for byte |
| `pkc/driver` | `driver.go` | build pipeline, `nasm`/`ld`, exit codes |
| `pkc/report` + `pkc/ui` | `report.go`, `ui/` | `-vb` output identical apart from timings |
| `pkc/tree` | `Tree` in `driver.go` | `tree` output |
| `pkc/main` | `cmd/packenger` | `build`, `run`, `asm`, `tree`, flags |

Expect 4–6k lines of Packenger. `pkgdoc` stays in Go.

## Phase 5: the bootstrap

```
stage0 = Go compiler
stage1 = stage0 compiles pkc      (built by Go)
stage2 = stage1 compiles pkc      (built by itself)
stage3 = stage2 compiles pkc
check:  stage2 asm == stage3 asm  (fixed point)
        stage1 asm == stage2 asm  (Go and pkc agree on pkc itself)
```

**Done when:**
- both checks pass;
- stage 2 passes the whole test suite and the comparison harness;
- CI runs all of it on every push.

## Working on both compilers together

- **A core change is only done when** it is in both compilers and the
  harness passes. Package-only changes need nothing extra.
- **Order:** implement a core change in Go first (it's the easier place to
  experiment), then port it to `pkc`.
- **The harness is the contract:** any difference in tokens, AST, NASM or
  errors fails CI.
- **Docs:** `pkc` and `rt` packages are documented like every other
  package and appear in the generated [package reference](registry/README.md).

## Risks

| risk | mitigation |
|---|---|
| no debugger for `pkc` | stderr printing, an `assert` package, and per-stage dumps that narrow a bug down to one module |
| byte-identical output is demanding | phase 0 makes Go deterministic first; compare at every stage, not just the final asm |
| the type system is the largest change | done first, while only Go has to change; `pkc` is written against the finished design |
| two implementations drift apart | the harness in CI; core changes only count as done when both pass |
| `pkc` speed | the current code generator (about `gcc -O0`) is fine for a compiler; register allocation later benefits both compilers |
| registry outages break bootstrapping | `file://` registry for CI and offline work |

## Order of work

0. Parity infrastructure
1. Type system
2. Other core features and new packages
3. `rt` runtime truck
4. `pkc` truck, module by module
5. Bootstrap fixed point in CI
6. Later: `pkgdoc` in Packenger

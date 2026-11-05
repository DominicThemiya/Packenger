# How the compiler works

The compiler knows almost nothing about the language. It knows how to
download and read package files, how to tokenize, and how to match the
patterns those packages declare. Every keyword, operator, statement and
instruction comes from a package.

## Pipeline

```
 source file
     │  use lines
     ▼
 1. fetch syntax packages ── registry: std.pkg, std/*.pkg (+ needs)
     │
     ▼
 2. build the grammar ────── keywords, symbols, statement/expr/infix/prefix tables
     │
     ▼
 3. lex + parse ──────────── two passes of pattern matching with backtracking
     │                         and type checking; expand packages are rewritten
     │                         and parsed again
     ▼  AST of node kinds (if, add, int, var, ...)
 4. fetch backend packages ─ <target>/<kind> for each kind used (+ @includes),
     │                         plus program, storage and toolchain
     │
     ▼
 5. render ───────────────── each node through its emit template → NASM or C
     │
     ▼
 6. toolchain ───────────── nasm + ld (bintables) or cc (ctables) → executable
```

1. **`use` lines** are read before anything else, because the file can't be
   tokenized until its syntax is known. Packages are downloaded level by
   level, in parallel: a truck's members, then their `needs`, and so on.
   The `code` sections of loaded library packages are tokenized too, and
   parsed ahead of the program file.
2. **The grammar** is assembled from every loaded syntax package:
   statements and expression forms indexed by their first literal, infix
   and prefix operator tables with precedences, the set of reserved
   keywords, the symbols the lexer accepts, and the block "closers" (the
   literals that end a block, such as `end` and `else`).
3. **Parsing** matches patterns. Statements starting with a keyword are tried
   first, then slot-first patterns, longest first. Expressions use
   precedence climbing (a Pratt parser) over the prefix, infix and postfix
   operator tables. Declarations made during a failed attempt are undone.
   Each slot is checked against its pattern's type constraint as it is
   parsed, binding type variables. A package with `expand` is matched,
   then its template is filled with the source of each slot and parsed
   again in its place.

   The file is parsed **twice**. The first pass is lenient about functions,
   structs, fields, globals and constants that are defined later in the
   file, and collects every function signature, struct layout, global and
   constant. The second pass parses again with
   all of them known, checks every type, and produces the tree. Calls are
   then checked against the signatures.
4. **Backend packages** are downloaded only for the node kinds present in
   the AST, plus anything they include with `@`.
5. **Rendering** picks, for each node, the first `emit` section whose
   condition holds and fills its placeholders, recursing into slots. Only
   functions reachable from the top-level code are rendered, in definition
   order, so unused library functions cost nothing. Data and support
   sections are collected and placed by the `bintables/program` template.
6. **Assembling** runs the commands of the target's `toolchain` package in
   a temporary directory: `nasm -f elf64` and `ld` for bintables, `cc -O2`
   for ctables. Where variables live comes from the `storage` package, so
   the compiler contains no assembly or C at all.

## Code map

| path | contents |
|---|---|
| [`cmd/packenger`](../cmd/packenger) | the command-line tool |
| [`cmd/pkgdoc`](../cmd/pkgdoc) | regenerates `docs/registry` |
| [`internal/registry`](../internal/registry) | downloading packages over HTTP or `file://`, in memory |
| [`internal/pkgfile`](../internal/pkgfile) | parsing package files: sections, patterns, emit headers |
| [`internal/lang/lexer.go`](../internal/lang/lexer.go) | tokenizer; symbols come from the loaded packages |
| [`internal/lang/parser.go`](../internal/lang/parser.go) | grammar tables, pattern matching, expansion, scopes, the two passes |
| [`internal/lang/types.go`](../internal/lang/types.go) | type expressions, constraints with type variables, struct layout |
| [`internal/lang/codegen.go`](../internal/lang/codegen.go) | rendering emit templates: placeholders, conditions, `@include`, `{each}` |
| [`internal/lang/driver.go`](../internal/lang/driver.go) | the pipeline, parallel fetching, `nasm`/`ld`, `tree` |
| [`internal/lang/report.go`](../internal/lang/report.go) | the build events behind `-vb` |
| [`internal/lang/dump.go`](../internal/lang/dump.go), [`internal/pkgfile/dump.go`](../internal/pkgfile/dump.go) | the `dump` formats |
| [`bootstrap/`](../bootstrap) | the comparison harness and its corpus |
| [`internal/ui`](../internal/ui) | the `-vb` report and error formatting |
| [`internal/pkgdoc`](../internal/pkgdoc) | the package reference generator and registry checks |
| [`registry/`](../registry) | the hosted packages: the `std`, `bintables`, `ctables`, `rt` and `pkc` trucks |

## Design notes

- **Why `use` comes first.** Even operator symbols are defined by packages,
  so the lexer needs the loaded packages before it can split `<=` from `<`.
- **Why syntax and backend are separate trucks.** The same `std` program can
  be compiled by a different backend truck (`--target`) without changing
  any syntax package. [ctables](registry/ctables.md) proves it: the same
  programs, compiler included, build through C with identical behaviour.
- **Why optimizations live in packages.** Variants such as
  `emit when right simple` and the `jfalse` sections keep instruction
  selection in `bintables`, where it can be changed without touching the
  compiler.
- **Code quality.** With bintables, expression results always pass through
  `rax`, and intermediate values through the stack. That is close to
  `gcc -O0`; register allocation is the main missing piece. ctables gets
  register allocation from `cc -O2`: `fib(35)` runs in 0.019 s instead of
  0.089 s.

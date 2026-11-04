# Dump formats

`packenger dump` prints a compiler stage as stable text. The formats are
the contract between the Go compiler and `pkc` (see the
[self-hosting plan](self-hosting.md)): for the same input, both must print
the same bytes. The [comparison harness](#the-comparison-harness) checks
this.

```
packenger dump tokens <file.pkg>    the token stream
packenger dump ast    <file.pkg>    the syntax tree after parsing and expansion
packenger dump asm    <file.pkg>    the generated NASM (same as `packenger asm`); only
                                    functions reachable from the top level, in definition order
packenger dump pkg    <package>     a registry package as parsed, e.g. std/if
```

On an error, a dump prints nothing on stdout, prints the error on stderr and
exits with status 1, like the other commands. `dump tokens` only needs the
file to tokenize, so it still works on files that don't parse.

## Common rules

- Lines end with `\n`; output ends with a newline.
- Strings are quoted byte by byte: printable ASCII (`0x20`–`0x7e`) other
  than `"` and `\` is written as is, every other byte as `\xHH` (lowercase
  hex), all inside `"`. For example, `héllo "x"` becomes
  `"h\xc3\xa9llo \x22x\x22"`.
- Positions are `file:line:col`, with lines and columns counted from 1 and
  columns in Unicode code points. The file is the source file's base name,
  `<package> (expand)` for tokens that come from an expand template, or
  `<package>.pkg` for library code (with the package file's line numbers).
- A constant's uses are copies of its value, with the value's positions.
- A private name `{$t}` in an expansion becomes `__t_<line>_<col>_<depth>`:
  the position of the expanded node and the expansion nesting depth.

## tokens

One token per line: `line:col kind [text]`.

| kind | text |
|---|---|
| `ident` | the identifier |
| `int` | the digits, with `_` removed |
| `str` | the quoted value (after escapes are processed) |
| `char` | the byte value of a character literal, e.g. `char 97` for `'a'` |
| `punct` | the symbol |
| `newline` | none |
| `eof` | none |

```
4:1 ident fn
4:4 ident fib
4:7 punct (
4:10 newline
```

`use` lines are blanked out before tokenizing, so they produce no tokens,
but line numbers still count them. Consecutive newlines collapse into one,
newlines inside parentheses are dropped, the stream always ends with
`newline` and then `eof`, and no newline comes first.

## ast

```
program frame locals=1 size=16
  - block #1 @fizzbuzz.pkg:3:1 expanded=std/for
    - let #2 @std/for (expand):1:1
      name: var #3 @fizzbuzz.pkg:3:5 : int i local 0
      value: int #4 @fizzbuzz.pkg:3:9 : int = 1
```

The first line describes the top-level frame. Then each top-level item
(statement, function, struct, global or constant) follows in source order,
library code first (packages in name order, then the program file), indented two spaces and
prefixed with `- `. Below a node come its slots, in the order they appear in
the pattern, prefixed with `name: `, then its items (the statements of a
block, the arguments of a call, ...), prefixed with `- `. Each level indents
by two more spaces.

A node line is:

```
kind #id @pos [: type] [detail] [expanded=pkg,...] [frame ...]
```

| part | meaning |
|---|---|
| `kind` | the package kind (`if`, `add`, ...) or a core kind: `block`, `int`, `str`, `var`, `ident`, `func`, `newfunc`, `args`, `params`, `type`, `newtype`, `fields`, `fielddecl`, `field`, `newconst`, `groups` (a repeated group's list), `group` (one occurrence) |
| `#id` | the node number (see below) |
| `@pos` | where the node starts |
| `: type` | the node's type, if it has one (expressions) |
| detail | `int`: `= 42`; `str`: `= "quoted"`; `var`: `name local N`, `name param N` or `name global N` (N is 0 for globals); `ident`/`func`/`newfunc`/`newtype`/`fielddecl`/`newconst`: the name; `field`: `name offset N` |
| `expanded=` | the packages that were expanded into this node, outermost first |
| `frame` | on function nodes: `frame name=N params=N locals=N size=N ret="type"` |

### Node numbering

Node numbers name labels in the generated code (`_L7_else`), so both
compilers must assign them identically:

- numbers are assigned **after** parsing (of the second pass; see
  [how the compiler works](architecture.md)), so parse attempts that failed
  leave no trace;
- the walk is pre-order over the top-level items in source order:
  a node, then its slots in pattern order, then its items;
- numbering starts at 1. The program node and the synthetic top-level block
  are 0.

### Variable slots

`local N` and `param N` are allocated in order of declaration within each
function (the top level counts as one). A declaration made during a failed
parse attempt gives its slot back, so the numbers depend only on the final
tree. Slots are not reused when a block ends. A frame's `size` is
`8 × locals`, rounded up to a multiple of 16.

## pkg

```
package bintables/lt
doc "< : `cmp`, then `setl` for a value or `jge` for a branch."
  | Two kinds of sections:
  |
emit section="" when slot=right op=simple arg=""
  | {left}
  | cmp rax, {right.operand}
```

In this order, each only if present:

| line | contents |
|---|---|
| `package <name>` | always |
| `doc <quoted summary>` | always (`""` if none), followed by the doc body |
| `needs a b ...` | |
| `type <name> <size>` | one line per declared type |
| `truck m1 m2 ...` | |
| `syntax <kind> prec=N assoc=left\|right [place] [return] [loop] [inloop] [top]` | then `  pattern <pattern>` for each pattern |
| `expand` | followed by the template |
| `code` | followed by the library source |
| `emit section=<quoted> [when slot=S op=O arg=<quoted>]` | for each emit section, followed by its lines |
| `data` | followed by its lines |
| `support` | followed by its lines |

Body lines are printed as `  | text`, or `  |` for an empty line.
Patterns are printed normalized: elements separated by single spaces, slots
as `{name: kind arg}` (`{name: newvar of slot}` for newvar and field),
optional groups as `[ ... ]`, repeated groups as `[ ... ]*`, and the result
as ` -> type`.

## The comparison harness

```sh
go test ./bootstrap/                  # Go compiler against itself
PKC=/path/to/pkc go test ./bootstrap/ # Go compiler against pkc
PKC=/path/to/pkc PKC_STAGES=pkg,tokens go test ./bootstrap/
                                      # only some stages: pkg, tokens, ast,
                                      # asm, run, report
```

It builds the Go compiler, then runs every compiler under test **three
times** on each input and fails on the first difference, showing the
differing line:

| input | compared |
|---|---|
| `bootstrap/testdata/programs/*.pkg`, `examples/*.pkg` and `tests/rt/*.pkg` | `dump tokens`, `dump ast`, `dump asm`, then running the program (skipped for files containing `// output: varies`) |
| `bootstrap/testdata/errors/*.pkg` | the same dumps, which must include a failure: error text and exit code |
| every package in `registry/` | `dump pkg` |
| `bootstrap/testdata/badreg/*.pkg` | `dump pkg` of broken packages: the error messages |
| a few command lines | `--help`, a bad `dump` stage, a missing argument |
| the examples and two test programs | `tree`, and `build`/`asm`/`run`/`dump` with `-vb`, with timings, timing bars and temporary paths masked (stage `report`) |

`TestBootstrap` then builds `pkc` with the Go compiler, builds it again with
that `pkc`, and once more with the result, and requires all three
executables to be byte-identical (skipped with `go test -short`).

The output of each run includes stdout, stderr and the exit status, with
`NO_COLOR=1` and a `file://` registry. Running each compiler more than once
catches nondeterminism even with only the Go compiler. Add a file to
`testdata/` to cover a new feature or error.

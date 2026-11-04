# Packenger

A compiled programming language where **everything is a package**, including
the syntax. `if`, `while`, `+`, `print` and `fn` are not built into the
compiler. Each is a package that declares its own syntax, inputs and output.
Packages are downloaded from a hosted registry at compile time, and the
**`bintables`** truck translates them to NASM, which becomes a native x86-64
Linux executable.

```
use std

fn fib(n)
  if n < 2
    return n
  end
  return fib(n - 1) + fib(n - 2)
end

print fib(35)
```

```
$ packenger run examples/fib.pkg
9227465
```

## Speed

fib(35), measured on the development machine:

| | time |
|---|---|
| Packenger (bintables) | 0.15 s |
| C, `gcc -O0` | 0.12 s |
| C, `gcc -O2` | 0.035 s |
| Python 3 | 2.1 s |

The executable is about 5 KB and uses no libc: it makes Linux syscalls directly.

## Packages and trucks

A **truck** is a collection of packages. Two trucks make up the language:

- **`std`** is the front end: what the language *looks like*.
- **`bintables`** is the back end: what each package *becomes* in NASM.

A program loads syntax with `use`:

```
use std          // the whole truck
use std/print    // or individual packages
```

### A syntax package declares syntax, inputs and output

```
// registry/std/if.pkg
doc Conditional. Any non-zero int is true.
syntax statement
  if {cond: expr int} {then: block} [else {else: block}] end
```

```
// registry/std/add.pkg
syntax infix 50 left
  {left: expr int} + {right: expr int} -> int
```

A pattern is made of literals (`if`, `end`, `+`), typed slots
`{name: kind [type]}` and optional groups `[ ... ]`. The compiler builds
its grammar entirely from the loaded patterns, including which symbols the
lexer recognizes.

| slot kind | matches |
|---|---|
| `expr [type]` | an expression, optionally of a required type (`int`, `str`) |
| `block` | statements up to the next literal (e.g. `end`) |
| `newvar [of slot]` | a name that declares a variable (typed like `slot`) |
| `var [type]` | an existing variable |
| `ident`, `func`, `newfunc` | a name, a called function, a defined function |
| `args`, `params` | comma-separated arguments / parameters |

The syntax kinds are `statement`, `function` (top level, gets its own stack
frame), `expr`, `infix <prec> [left|right]` and `prefix <prec>`.

### A package can be made only of other packages

`for` has no backend. It expands into `let`, `while`, `<=`, `=` and `+`:

```
// registry/std/for.pkg
needs std/let std/while std/le std/assign std/add
syntax statement
  for {i: newvar} = {from: expr int} to {to: expr int} {body: block} end
expand
  let {i} = {from}
  while {i} <= {to}
    {body}
    {i} = {i} + 1
  end
```

### bintables: packages → NASM

Every node kind is rendered by the `bintables` package of the same name.
That includes integer literals, variables and the program skeleton
(`bintables/program`):

```
// registry/bintables/add.pkg
emit when right simple
  {left}
  add rax, {right.operand}
emit
  @binop
  add rax, rbx
```

- `{slot}` inserts the slot's code, and its value ends up in `rax`.
  `{slot.addr}`, `{slot.label}`, `{slot.value}`, `{slot.operand}`, `{frame}`
  and similar placeholders insert attributes.
- `{L.name}` inserts a label unique to this use. `{D.name}` does the same
  for the data section.
- `@pkg` inlines another bintables package. `{each args reverse}` ...
  `{end}` loops over a list.
- `emit when <slot> simple | is <kind> | has <section> | <type> | empty`
  picks a variant. This is how the truck does its optimizations, all inside
  packages. Comparisons provide an `emit jfalse` section, so `if` and `while`
  branch on the CPU flags directly:

```
// registry/bintables/if.pkg
emit when cond has jfalse
  {cond!jfalse L.else}
  {then}
  jmp {L.end}
  {L.else}:
  {else}
  {L.end}:
```

Further sections are `data` (per-use lines in `.data`) and `support` (code
emitted once if the package is used, e.g. `print`'s integer formatting).
Retargeting the language means writing another truck and passing
`--target`.

### Extending the language

Adding syntax means adding packages, with no change to the compiler. From
the test suite:

```
// unless.pkg: front end only
needs std/if std/not
syntax statement
  unless {cond: expr int} {body: block} end
expand
  if !({cond})
    {body}
  end
```

```
// std/max.pkg + bintables/max.pkg: a new operator
syntax infix 45 left
  {left: expr int} max {right: expr int} -> int

emit
  @binop
  cmp rax, rbx
  cmovl rax, rbx
```

## The `std` language

```
use std

fn is_prime(n)              // functions: int parameters, int result
  if n < 2
    return 0
  end
  let d = 2                 // declare
  while d * d <= n
    if n % d == 0
      return 0
    end
    d = d + 1               // assign
  end
  return 1
end

let count = 0
for n = 1 to 100000
  if is_prime(n)
    count = count + 1
  end
end
print "primes below 100000:"   // print ints and strings
print count
return 0                       // top-level return = exit code
```

Operators, from lowest to highest precedence: `||`, `&&`, `== !=`,
`< <= > >=`, `+ -`, `* / %`, and the prefix operators `! -`. Comments start
with `//`. Statements end at a newline, except inside parentheses.

## Usage

```
go build -o bin/packenger ./cmd/packenger

packenger run   examples/primes.pkg
packenger build examples/fib.pkg -o fib    # native executable
packenger asm   examples/fib.pkg           # show the NASM
packenger tree  examples/primes.pkg        # which packages built it
```

Flags: `--registry URL` (or `PACKENGER_REGISTRY`), `--target TRUCK`
(default `bintables`), `-o FILE`, `-v` (log fetches).
Building requires `nasm` and `ld`.

By default packages come from
`https://raw.githubusercontent.com/DominicThemiya/Packenger/main/registry`.
Fetches run level by level in parallel and are kept in memory only.
Backend packages are fetched only for the constructs a program uses. For
local work on the registry itself:

```
PACKENGER_REGISTRY=file://$PWD/registry packenger run examples/hello.pkg
```

## Known limits / next steps

- **Codegen** keeps every value on the stack, with expression results in
  `rax` (roughly `gcc -O0`). Register allocation would close most of the
  gap to `-O2`, and could live in bintables as more `when` variants.
- **Types**: only `int` and `str`. Functions take and return `int`.
- **Syntax**: no `else if` chain yet (nest `if`s). Expansions paste source
  tokens, so `for`'s upper bound is re-evaluated every iteration.
- **Reproducibility**: packages are fetched from a moving branch. A lockfile
  or `use std@v1` pins would fix that.
- **Platform**: x86-64 Linux only, which is what bintables targets.

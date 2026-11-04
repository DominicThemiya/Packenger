# Packages and trucks

In Packenger the language itself is made of packages. A **package** is one
`.pkg` file in the registry; a **truck** is a package that lists other
packages. There are two roles:

- **syntax packages** (the `std` truck) say what the language looks like:
  a pattern, the types of its slots and the type it produces;
- **backend packages** (the `bintables` truck) say what each construct
  becomes in NASM.

A syntax package and its backend package are paired by name: nodes made by
`std/if` are rendered by `bintables/if`. A syntax package can instead
`expand` into other syntax packages, in which case it needs no backend.

## File format

A package file is a list of sections. A section header starts in column 1;
its body is indented by two spaces. Lines starting with `//` are comments.

| section | purpose |
|---|---|
| `doc <summary>` | one-line summary, then an indented Markdown body |
| `needs <pkg> ...` | other packages this one builds on; they're fetched too |
| `type <name> <size>` | declare a primitive type (size 1 or 8 bytes) |
| `truck` | makes the file a truck; the body lists member names |
| `syntax <kind> [prec] [left\|right] [place] [return]` | one or more patterns |
| `expand` | rewrite each use into other packages' syntax |
| `emit [<section>] [when <cond>]` | NASM template (backend packages) |
| `data` | NASM lines for the data section, once per use |
| `support` | NASM lines emitted once per program if the package is used |

## Syntax

```
// registry/std/if.pkg
syntax statement
  if {cond: expr bool} {then: block} [else {else: block}] end
```

### Kinds

| kind | used as | notes |
|---|---|---|
| `statement` | a line of a block | |
| `function` | top-level definition | gets its own stack frame and variables; a `type` slot named `ret` is its result type |
| `struct` | top-level type declaration | needs a `newtype` and a `fields` slot |
| `expr` | an expression | e.g. a call: `{callee: func} ( {args: args} )` |
| `infix <prec>` | binary operator | pattern must be `{a: expr} OP {b: expr}` |
| `prefix <prec>` | unary operator | pattern must be `OP {a: expr}` (or `OP {a: place}`) |
| `postfix <prec>` | operator after an expression | pattern must start `{a: expr} OP ...`, e.g. `{x: expr [T]} \[ {i: expr int} \]` |

Higher precedence binds tighter. Infix operators are left-associative
unless the header says `right`. Several patterns of one package may share an
operator; the operand types pick the pattern (e.g. `<` for `int` and for
`u8`).

Header flags:

- `place`: the construct can be assigned to and have its address taken
  (fields, slice elements, `*p`). Its backend package must have an
  `emit addr` section that leaves the address in `rax`.
- `return`: a return statement. Its slot named `value` is checked against
  the enclosing function's result type, and leaving it out is only allowed
  in functions without a result.

### Patterns

A pattern is a sequence of:

- **literals**: keywords (`if`, `end`) and symbols (`+`, `(`). Every
  literal becomes reserved, and symbols are added to the lexer. Write
  `\[` for a literal bracket;
- **slots** `{name: kind [arg]}`: the parts the user writes;
- **optional groups** `[ ... ]`, which must start with a literal. The
  group is taken when that literal is present.

A pattern can end with `-> type` (the type it produces) or `-> {slot}` (the
same type as that slot).

### Slot kinds

| slot | matches | arg |
|---|---|---|
| `expr` | an expression with a value | a type constraint (below); `any` also allows calls without a result |
| `place` | an expression that can be assigned to | a type constraint |
| `block` | statements up to the next literal | — |
| `type` | a type, e.g. `int`, `ptr Node`, `[u8]` | a type constraint |
| `newvar` | a name; declares a variable | `of <slot> ...`: take the type of the first of those slots present |
| `var` | an existing variable | a type constraint |
| `ident` | any name | — |
| `func` | the name of a function; has the function's result type | — |
| `newfunc` | a name; defines a function | — |
| `args` | comma-separated expressions | — |
| `params` | comma-separated `name: type` parameters | — |
| `newtype` | a name; declares a struct type | — |
| `fields` | `name: type` lines up to the next literal | — |
| `field` | a field name of the struct pointed to by a slot | `of <slot>` (required) |

A `block` or `fields` slot must be followed by a literal, such as `end` or
`else`, so the parser knows where it stops.

### Type constraints and results

A constraint is a type, possibly with **type variables** (single capital
letters) and **slot references**:

| constraint | accepts |
|---|---|
| `int` | exactly `int` |
| `[T]` | any slice, binding `T` to its element type |
| `ptr T` | any pointer, binding `T` |
| `ptr ?` | any pointer (`?` matches anything) |
| `T` | anything; a later `T` must be the same type |
| `{slot}` | the type of another slot, e.g. `{value: expr {target}}` |
| `any` | anything, including no value |

The result after `->` can use the same variables and references:
`* {p: expr ptr T} -> T`, `& {x: place} -> ptr {x}`,
`new {t: type} -> ptr {t}`. Constraints are checked as each slot is parsed,
so a mismatch makes the pattern fail and the next one is tried. The error
for a pattern that matched but broke a type rule is reported in preference
to syntax errors from other patterns.

### How statements are chosen

Patterns that start with a literal are tried for that keyword first. Then
patterns that start with a slot are tried, longest first (so assignment
`{name: var} = {value: expr}` wins over a bare expression statement). If
nothing matches, the error that got furthest into the line is reported.

## Expansion: packages made of packages

A package with an `expand` section is rewritten every time it is used: the
source written in each slot is pasted into the template, and the result is
parsed again with the loaded packages.

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

- Expansions can use packages that expand themselves, up to 64 levels deep.
- List the packages the template uses in `needs`, so they're loaded even
  when the user loads only this package.
- Expression slots are pasted inside parentheses, so precedence is kept.
- **Limitations:** names in the template aren't private (two expansions in
  one block that declare the same helper variable collide); a slot used
  twice is evaluated twice; `function` packages can't expand.

## Backend packages: emit templates

```
// registry/bintables/add.pkg
emit when right simple
  {left}
  add rax, {right.operand}
emit
  @binop
  add rax, rbx
```

The first `emit` section whose condition holds is used. A template is NASM
with placeholders.

### Placeholders

| placeholder | becomes |
|---|---|
| `{slot}` (alone on a line) | the slot's generated code; expressions leave their value in `rax` |
| `{slot.addr}` | a variable's stack slot, e.g. `qword [rbp-8]` |
| `{slot.mem}` | the same without the size, e.g. `[rbp-8]`, for `lea` |
| `{slot.operand}` | a simple operand: the integer, or the variable's address |
| `{slot.value}` | an integer literal's value |
| `{slot.label}` | a function's label, e.g. `pk_fib` |
| `{slot.name}` | a variable or function name |
| `{slot.count}`, `{slot.stack}` | number of items in a list, and 8 × that |
| `{slot.size}` | bytes the slot's type occupies in memory (1 or 8) |
| `{slot.elemsize}` | size of the element of a slice, the pointee of a pointer, or 1 for `str` |
| `{slot.alloc}` | bytes to allocate for a `type` slot: a struct's full size |
| `{slot.offset}` | a `field` slot's offset in its struct |
| `{slot.len}`, `{slot.bytes}` | a string's length and its bytes as `db` values |
| `{frame}` | the stack space for a function's (or the program's) variables |
| `{L.name}` | a label unique to this use, e.g. `{L.end}:` |
| `{D.name}` | a data label unique to this use |
| `{a.b}` | slot `b` of slot `a`, e.g. `{cond.left}`; `self` is the node itself |
| `{slot!section arg}` | the slot rendered with its package's named section, passing `arg` as `{target}` |
| `{each list [reverse]}` … `{end}` | repeat lines for each item; `{it}` is the item |
| `@pkg [section]` | inline another backend package's template |

A missing optional slot (like `{else}` with no `else`) renders as nothing.

### Conditions

| condition | true when |
|---|---|
| `when <slot> <type>` | the slot's type is `<type>`, e.g. `when value str` |
| `when <slot> is <kind>` | the slot's node kind is `<kind>`, e.g. `when right is int` |
| `when <slot> has <section>` | the slot's backend package has that section |
| `when <slot> simple` | the slot is a 32-bit integer literal or a variable |
| `when <slot> empty` | the list slot has no items |
| `when <slot> missing` | an optional slot was not given |
| `when <slot> size <n>` | the slot's type occupies `n` bytes; `when self size 1` picks byte loads |

Conditions are how the backend optimizes. For example, `if` uses
`when cond has jfalse` to branch straight on a comparison's flags:

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

### Core node kinds

A few node kinds come from the lexer rather than from a syntax package, but
they are still rendered by backend packages: `int` (number and character
literals), `str` (string literals), `var` (variable reads; its `emit addr`
section serves `&x` and assignment) and `program` (the skeleton around
everything). See [bintables/program](registry/bintables/program.md).

The slot kinds `type`, `field`, `newtype`, `fields`, `ident`, `func`,
`newfunc`, `args` and `params` are data for their parent's template, not
code; use their attributes. These names are reserved and can't be package
names.

The conventions the `bintables` packages agree on (registers, stack layout,
calling convention) are listed on the
[bintables truck page](registry/bintables.md).

## Trucks

```
// registry/std.pkg
doc The standard language ...
truck
  fn
  call
  if
  ...
```

Members live next to the truck: `std/if` is `registry/std/if.pkg`. A member
can itself be a truck, giving nested trucks such as `extra/loops/twice`.
A truck can also have `needs`.

## Documentation

Every package documents itself in its `doc` section:

```
doc Conditional: if cond ... [else ...] end
  Runs the first block if the condition is non-zero, otherwise the `else`
  block, if there is one.

  ```
  if x > 3
    print "big"
  end
  ```
```

The summary is the rest of the header line. The body is Markdown, indented
two spaces; blank lines, `//` and indentation inside it are kept. The
[package reference](registry/README.md) is generated from these, along with
each package's syntax, slots, expansion and emit variants. See
[Publishing packages](publishing.md#documentation).

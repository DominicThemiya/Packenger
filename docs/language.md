# The std language

Everything here is defined by the [`std` truck](registry/std.md), not by the
compiler. Load it at the top of every file:

```
use std
```

You can also load only some packages, e.g. `use std/print`, and add your own
syntax by loading other packages (see [packages and trucks](packages.md)).

## Program structure

- A file is a list of statements, one per line, run from top to bottom.
- Functions (`fn`) and structs (`struct`) are declared at the top level and
  can be used before their declaration.
- Blocks are closed with `end` instead of braces.
- Comments start with `//`.
- Newlines end statements, except inside parentheses, so a long expression
  can be wrapped in `( ... )`.

```
use std

struct Node
  value: int
  next: ptr Node
end

fn sum(list: ptr Node) -> int
  let total = 0
  while list != null
    total = total + list.value
    list = list.next
  end
  return total
end

let a = new Node
a.value = 40
a.next = new Node
a.next.value = 2
print sum(a)      // 42
```

## Types

Packenger is statically typed: every expression has a type, checked at
compile time.

| type | values | written as |
|---|---|---|
| `int` | 64-bit signed integers; overflow wraps | `42`, `1_000_000`, `-7` |
| `u8` | bytes 0–255 | `'a'`, `'\n'`, `65 as u8` |
| `bool` | truth values | `true`, `false`, comparisons |
| `str` | immutable strings | `"hi\n"` (Go-style escapes) |
| `ptr T` | pointer to a `T`, or `null` | `new T`, `&x`, `null` |
| `[T]` | slice: a growable-capacity array of `T` | `make([T], n)` |
| struct | a record with named fields | `struct Name ... end`, used as `ptr Name` |

The primitive types come from [std/types](registry/std/types.md). Every value
fits in one machine register; strings, slices and struct pointers are
addresses of heap or static memory.

Conditions are strictly `bool`: `if n` on an `int` is a type error; write
`if n != 0`.

## Variables

```
let x = 10                  // the type comes from the value: int
let name: str = "pk"        // or from an annotation
let head: ptr Node = null   // annotations give null a pointee type
x = x + 1                   // assignment; the value must have x's type
```

Variables belong to the block they're declared in. An inner block can shadow
an outer variable, but declaring the same name twice in one block is an
error. Functions can't see top-level variables. Variables hold values of
any type except a struct itself: use `ptr Name`.

## Functions

```
fn square(n: int) -> int
  return n * n
end

fn greet(name: str)       // no result
  print name
end

greet("hi")
print square(9)
```

- Every parameter has a type, and `-> type` gives the result type. A
  function without a result is called as a statement.
- Argument counts and types are checked. Functions can be used before
  they're defined, and can be recursive or mutually recursive.
- `return value` must match the result type; a function without a result
  uses a bare `return`.
- Falling off the end of a function that has a result returns zero.
- At the top level, `return n` ends the program with exit code `n`.

## Structs and pointers

```
struct Point
  x: int
  y: int
end

let p = new Point     // ptr Point, fields zeroed
p.x = 3
p.y = p.x * 2

fn bump(counter: ptr int)
  *counter = *counter + 1
end

let n = 0
bump(&n)              // & takes the address of a variable, field or element
```

- `new T` allocates a zeroed `T` on the heap and gives a `ptr T`. Memory is
  never freed.
- `p.field` reads or assigns a field through a struct pointer.
- `*p` reads or assigns what a pointer points to; `&x` makes a pointer.
- `null` is the empty pointer; `p == null` tests for it. Reading a field
  through `null` crashes the program.

## Slices and strings

```
let squares = make([int], 5)   // 5 zeroed ints
for i = 0 to 4
  squares[i] = i * i
end
print len(squares)             // 5
print squares[4]               // 16

let s = "hello"
print len(s)                   // 5: the length in bytes
print s[0] == 'h'              // true: indexing a str gives a u8
print s == "hello"             // true: strings compare by content
```

Indexes are checked: an index outside `0 .. len - 1` stops the program with
`panic: index out of range` and exit status 2. Strings are immutable: their
bytes can be read but not assigned.

## Conversions

`x as T` converts explicitly; there are no implicit conversions.

```
print ('a' as int) + 1   // 98
print 300 as u8          // 44: the low byte
print 5 as bool          // true: non-zero
```

Converting to `u8` keeps the low byte, and to `bool` tests for non-zero.
Other conversions (between pointer types, or pointers and `int`) keep the
bits, so they can create unsafe pointers.

## Control flow

```
if x > 5
  print "big"
else
  print "small"
end

while x > 0
  x = x - 1
end

for i = 1 to 10      // inclusive on both ends; i is an int
  print i
end
```

There is no `else if` yet; nest an `if` inside `else`.

## Operators

Lowest to highest precedence; all infix operators are left-associative.

| precedence | operators | operand types | packages |
|---|---|---|---|
| 10 | `\|\|` | `bool` | [or](registry/std/or.md) |
| 20 | `&&` | `bool` | [and](registry/std/and.md) |
| 30 | `==` `!=` | any two of the same type | [eq](registry/std/eq.md), [ne](registry/std/ne.md) |
| 40 | `<` `<=` `>` `>=` | `int` or `u8` | [lt](registry/std/lt.md), [le](registry/std/le.md), [gt](registry/std/gt.md), [ge](registry/std/ge.md) |
| 50 | `+` `-` | `int` | [add](registry/std/add.md), [sub](registry/std/sub.md) |
| 60 | `*` `/` `%` | `int` | [mul](registry/std/mul.md), [div](registry/std/div.md), [mod](registry/std/mod.md) |
| 70 | prefix `!` `-` | `bool`, `int` | [not](registry/std/not.md), [neg](registry/std/neg.md) |
| 80 | prefix `*` `&` | pointer, place | [deref](registry/std/deref.md), [addr](registry/std/addr.md) |
| 85 | `as` | any | [as](registry/std/as.md) |
| 90 | `a[i]` `p.x` | slice or `str`, struct pointer | [index](registry/std/index.md), [dot](registry/std/dot.md) |

Comparisons produce `bool`. `&&` and `||` short-circuit. `/` rounds toward
zero and `%` takes the sign of the left operand, as in C.

## Output

```
print 42        // ints and u8s print as numbers
print "text"
print 1 < 2     // true
```

`print` writes the value and a newline to stdout.

## Errors

Compile errors point at the source:

```
  ✗ error e.pkg:8:15: left side of '+' must be int, but it is str

    8 │ print twice(s + 1)
      │               ^
```

## Limits

- Memory is never freed.
- No `else if`, `break` or `continue` yet; no globals or constants.
- No input, and output is `print` only.
- No generic functions; slices and pointers are the only parameterized types.

For each construct's exact syntax and edge cases, see its page in the
[package reference](registry/README.md).

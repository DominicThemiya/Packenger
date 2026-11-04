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
- Function definitions (`fn`) can appear anywhere at the top level.
- Blocks are closed with `end` instead of braces.
- Comments start with `//`.
- Newlines end statements, except inside parentheses, so a long expression
  can be wrapped in `( ... )`.

```
use std

// greatest common divisor
fn gcd(a, b)
  while b != 0
    let t = b
    b = a % b
    a = t
  end
  return a
end

print gcd(84, 36)
```

## Values and types

| type | written as | notes |
|---|---|---|
| `int` | `42`, `1_000_000` | 64-bit signed; overflow wraps around |
| `str` | `"hi\n"` | Go-style escapes; used with `print` and `let` |

Truth values are `int`s: 0 is false, anything else is true. Comparisons and
logic operators produce 0 or 1.

## Variables

```
let x = 10        // declare (std/let); the type comes from the value
x = x + 1         // assign (std/assign)
```

Variables belong to the block they're declared in. An inner block can shadow
an outer variable, but declaring the same name twice in one block is an
error. Functions can't see top-level variables.

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

for i = 1 to 10      // inclusive on both ends
  print i
end
```

There is no `else if` yet; nest an `if` inside `else`.

## Functions

```
fn square(n)
  return n * n
end

print square(9)
```

Parameters and results are `int`s. Functions can be called before they're
defined and can be recursive; argument counts are checked at compile time.
A function that ends without `return` returns 0. At the top level,
`return n` ends the program with exit code `n`.

## Operators

Lowest to highest precedence; all infix operators are left-associative.

| precedence | operators | packages |
|---|---|---|
| 10 | `\|\|` | [or](registry/std/or.md) |
| 20 | `&&` | [and](registry/std/and.md) |
| 30 | `==` `!=` | [eq](registry/std/eq.md), [ne](registry/std/ne.md) |
| 40 | `<` `<=` `>` `>=` | [lt](registry/std/lt.md), [le](registry/std/le.md), [gt](registry/std/gt.md), [ge](registry/std/ge.md) |
| 50 | `+` `-` | [add](registry/std/add.md), [sub](registry/std/sub.md) |
| 60 | `*` `/` `%` | [mul](registry/std/mul.md), [div](registry/std/div.md), [mod](registry/std/mod.md) |
| 70 | prefix `!` `-` | [not](registry/std/not.md), [neg](registry/std/neg.md) |

`&&` and `||` short-circuit. `/` rounds toward zero and `%` takes the sign
of the left operand, as in C.

## Output

```
print 42
print "text"
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

- Only `int` and `str`; no arrays, structs or floats yet.
- Function parameters and results are `int`.
- Assigning a value of a different type to a variable isn't caught yet.
- No input; output is `print` only.

For each construct's exact syntax and edge cases, see its page in the
[package reference](registry/README.md).

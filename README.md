# Packenger

A programming language where **everything is a package**. A number is a
package, `if` is a package, `true` is a package. Each package is a
composition of other packages. The compiler downloads them from a hosted
registry at compile time and unwraps the whole tree until only the two
bootstrap packages remain: **`0`** and **`1`**.

```
$ packenger run examples/factorial.pkg
120
$ packenger tree examples/factorial.pkg
factorial.pkg  (1292 bits)
├── factorial.pkg#fact  (959 bits)
│   ├── Y  (200 bits)
│   │   ├── S = 1 1 1 1 1 0 0 0  [bootstrap bits]
│   │   ├── K = 1 1 1 0 0  [bootstrap bits]
│   │   └── I  (20 bits)
│   ├── if  (20 bits)
│   ├── is-zero  (89 bits)
│   ...
```

## Why it bottoms out in `0` and `1`

If every package unwraps to bits, the bits need a meaning. Packenger uses
[Jot](https://en.wikipedia.org/wiki/Iota_and_Jot), a Turing-complete
language in which *every* bit string is a program and each bit is an
operator applied to the program built so far:

| package | meaning                       |
|---------|-------------------------------|
| (empty) | `I`                           |
| `w 0`   | `[w] S K`                     |
| `w 1`   | `λx.λy.[w](x y)`              |

With only those two rules, the registry defines the two classic combinators
as plain bit sequences:

```
# registry/K.pkg          # registry/S.pkg
@11100                    @11111000
```

From `S` and `K` you can build every other computation. They have one key
property: appending such a string to any program *applies* that program to
it (`[w·bits] = [w] T`). The compiler can therefore encode application
`f x` as `1 ++ bits(f) ++ bits(x)`, and a whole program becomes one bit
string. The machine that runs it knows nothing except the two rules above.

## The language

A file is a body expression, optionally preceded by file-local helper
packages (`name := expr`). Every top-level item starts in column 1.

```
# output: string
fact := Y (\self n. if (is-zero n) 1 (mul n (self (pred n))))

append "5! = " (show (fact 5))
```

| syntax       | meaning                                                         |
|--------------|-----------------------------------------------------------------|
| `if`, `mul`  | a reference to a hosted package (or a lambda-bound name)        |
| `f x y`      | application                                                     |
| `\x y. body` | abstraction; the compiler eliminates it into the `S`/`K`/`I` packages |
| `42`         | number; built from the `zero`/`succ`/`dbl` packages via its binary digits |
| `"hi"`       | string; a list of code points built from `cons`/`nil`          |
| `@11100`     | raw bits: a sequence of the bootstrap packages `0` and `1`      |
| `# output: K` | directive telling `run` how to read the result                 |

There are no keywords. `if`, `true`, `Y` and so on are ordinary packages in
the [registry](registry/). A package can't depend on itself, because
unwrapping inlines it forever, so recursion goes through the `Y` package.
The compiler reports dependency cycles.

## The compiler

1. **Parse** the local file.
2. **Traverse**: every name that isn't bound locally is fetched from the
   registry as `<registry>/<name>.pkg`. Fetching runs level by level, in
   parallel, and keeps everything in memory. Nothing is stored on disk.
3. **Compile** each package once into a combinator tree whose leaves are
   other packages (bracket abstraction with the K and η rules).
4. **Unwrap** the tree down to `0`/`1`, memoizing each package's bits.
5. **Validate** every raw-bit literal (`@...`) to be applicative.

The runtime decodes the bits into a combinator graph and reduces it lazily,
with sharing. It reads the result by applying it to host markers. For
example, a Church numeral is applied to a host `+1` and `0`.

## Usage

```
go build -o bin/packenger ./cmd/packenger

packenger run   examples/hello.pkg            # Hello, Packenger!
packenger run   examples/squares.pkg          # [1, 4, 9, ..., 100]
packenger build examples/factorial.pkg -o fact.jot
packenger run   fact.jot --as nat             # run raw bits, no registry needed
packenger tree  examples/report.pkg
packenger eval  'eq (mul 6 7) 42' --as bool   # true
```

Flags: `--registry URL`, `--as nat|bool|list|string|term`, `--max-steps N`,
`-v` (log fetches and step counts).

### Registry

By default packages are fetched from
`https://raw.githubusercontent.com/DominicThemiya/Packenger/main/registry`,
which is the [`registry/`](registry/) folder of this repo once it's pushed.
Override it with `--registry` or `PACKENGER_REGISTRY`. A `file://` URL
works for developing the registry itself:

```
PACKENGER_REGISTRY=file://$PWD/registry packenger run examples/report.pkg
```

To publish a package, add `<name>.pkg` to the registry repo. Names can be
namespaced with `/` (e.g. `list/zip` → `registry/list/zip.pkg`).

## Known limits / next steps

- **Speed.** Church numerals are unary, so arithmetic is O(n), and `div`
  and `show` are slow for numbers in the thousands. Avoid repeated
  `sub`/`pred` chains: their results are closures that redo work each time
  they are applied. See `registry/divmod.pkg` for the pattern that shares.
- **Size.** Bits grow with how often packages are used, because every use
  inlines a copy (`hello.pkg` is about 17 kbit).
- **Versioning / integrity.** Packages are fetched from a moving branch.
  A lockfile with content hashes, or `name@ref` pins, would make builds
  reproducible.
- **I/O.** Results are observed only at the end (`--as`). There are no
  effects yet.

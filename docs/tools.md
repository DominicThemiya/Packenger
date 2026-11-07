# Tools

Programs for working with Packenger and its registries, written in
Packenger. They live in [`tools/`](../tools) and run like any program:

```sh
packenger run tools/<tool>.pkg -- [arguments]
```

| tool | does |
|---|---|
| [`treestat.pkg`](../tools/treestat.pkg) | measures a package tree: sizes, what each truck costs to use, dependencies, grammar, backend coverage |

## treestat

`treestat` downloads every package of a registry, the way the compiler
does, and reports how heavy the tree is.

```sh
packenger run tools/treestat.pkg                                  # the universal tree, on GitHub
packenger run tools/treestat.pkg -- http://localhost:6464         # a self-hosted tree
packenger run tools/treestat.pkg -- https://example.com/pkgs std  # a host without index.txt
```

| section | shows |
|---|---|
| overview | packages, trucks, bytes, lines, the largest package, and how long the downloads took |
| contents | lines by section: `doc`, `syntax` patterns, `expand`, `emit` and `code` |
| trucks | each truck's kind (front-end, backend or library), size and largest package |
| what it costs to use | everything a `use` line loads: packages, bytes, and rounds of downloads (the depth of the dependency graph) |
| most depended on | the packages most others `need` or `@include` |
| language | the patterns, keywords and symbols each front-end truck adds to the grammar, and how many are new |
| backends | how many front-end packages each backend translates, and which are missing |
| problems | packages that are referenced but missing, files that don't parse, and `needs` cycles |

It exits with status 1 if there are problems, so it can run in CI.

```
WHAT IT COSTS TO USE  everything a use line loads
  use               pkgs       size   rounds
  pkc                 78   249.6 KB        4  ████████████████████
  rt                  63    54.3 KB        4  ████
  webtables           59    35.0 KB        2  ██
  bintables           60    25.5 KB        2  ██
  std                 52    21.1 KB        2  █
  ctables             59    18.7 KB        2  █
  stdx                31    14.5 KB        3  █
```

### Finding the packages

A registry is just files over HTTP, and a folder can't be listed over
HTTP. So `treestat` reads `<registry>/index.txt`, a list of package names,
one per line:

- the universal registry has one, written by `go run ./cmd/pkgdoc`;
- [`examples/web/server.pkg`](../examples/web/server.pkg) generates one
  for whatever folder it serves;
- for any other host, publish an `index.txt`, or name the packages to start
  from after the registry URL.

From those it follows truck members, `needs` and backend `@include`s, so it
also finds packages that are referenced but not listed.

### Trees built on other trees

A self-hosted tree usually holds only its own packages and relies on the
universal tree for `std` and `rt`, the way a program's
[package tree](publishing.md#package-trees) routes only some names to it.
`treestat` does the same: a package the registry doesn't have is looked up
in a fallback registry, the universal tree by default. Such packages are
counted as **external**: they're part of what using a package costs, but
not of the tree's own sizes, trucks, grammar and backends.

| flag | effect |
|---|---|
| `--fallback <url>` | look up missing packages there instead |
| `--no-fallback` | report them as missing |

`treestat` is built on the compiler's own packages:
[pkc/fetch](registry/pkc/fetch.md) downloads (one `curl --parallel` per
round) and [pkc/pkgfile](registry/pkc/pkgfile.md) parses, so it reads
packages exactly as `pkc` does. It needs `curl`.

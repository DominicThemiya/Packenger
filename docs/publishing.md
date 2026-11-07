# Publishing packages

## The registry

The registry is a folder of package files served over HTTP. By default it is
the [`registry/`](../registry) folder of this repository, served by GitHub:

```
https://raw.githubusercontent.com/DominicThemiya/Packenger/main/registry
```

Package `std/if` is fetched from `<registry>/std/if.pkg`, and a truck `std`
from `<registry>/std.pkg`. Packages are downloaded at compile time and kept
in memory only; nothing is cached on disk. Each round of downloads (the
packages named so far, then the ones they need, and so on) runs in
parallel: goroutines in the Go compiler, one `curl --parallel` in `pkc`.

Any static file host works. Point the compiler at it with `--registry URL`
or `PACKENGER_REGISTRY`, or route some packages to it from a
[package tree](#package-trees). For working on packages locally use a
`file://` URL:

```sh
export PACKENGER_REGISTRY=file://$PWD/registry
```

Tools such as [treestat](tools.md#treestat) find a registry's packages
through `<registry>/index.txt`, one name per line, since a folder can't be
listed over HTTP. `go run ./cmd/pkgdoc` keeps the universal registry's up
to date, and `server.pkg` generates one; on other static hosts, publish
one alongside the packages.

## Package trees

A program can say where its packages come from. With the line
`use pkg_tree` among its `use` lines, the compiler reads `pkg_tree.pkg`
from the program's folder before fetching anything:

```
// pkg_tree.pkg
tree
  acme   http://localhost:6464
  *      https://raw.githubusercontent.com/DominicThemiya/Packenger/main/registry
```

```
// app.pkg
use pkg_tree
use std
use acme          // from localhost:6464
```

- **Routes go by name prefix.** `acme` covers the package `acme` and
  everything under it, such as `acme/banner`. When several prefixes match,
  the longest wins, so `acme/net` can be routed somewhere other than the
  rest of `acme`.
- **`*` is the default** for packages no route matches. Without a `*` line
  they come from where they would anyway: `PACKENGER_REGISTRY`, else the
  universal registry. So a tree that only adds your own packages needs no
  `*` line.
- **Registries** are `http://`, `https://` or `file://` URLs.
- **The format** is like a package file: a `tree` section, with one
  `<prefix> <registry>` line per route, indented, and `//` comments.
  `pkg_tree` is not fetched, so it can't be the name of a hosted package.
- **What wins:** `--registry` replaces the whole tree (useful for pointing
  everything at a mirror). Otherwise a [lock file](#lock-files) records the
  tree's routes and is used instead of it; run `packenger lock` again after
  changing the tree. `packenger run -vb` shows the routes in use.

## Hosting a registry

A registry is just files, so anything that serves a folder over HTTP will
do: a web server, GitHub, or [`examples/web/server.pkg`](../examples/web/server.pkg),
a registry server written in Packenger:

```sh
packenger build examples/web/server.pkg -o pkg-server
./pkg-server my-registry                  # http://127.0.0.1:6464
./pkg-server my-registry 8000 0.0.0.0     # port 8000, reachable from other machines
```

It serves `<folder>/<name>.pkg` at `/<name>.pkg`, at `/index.txt` a list
of every package, and at `/` a page listing every package with its summary
and the `pkg_tree.pkg` lines for using it. Lay the folder out like [`registry/`](../registry): a truck
`acme` is `acme.pkg`, and its member `banner` is `acme/banner.pkg`.
[`examples/web`](../examples/web) has a small one, and a program that uses
it.

## Lock files

Without a lock, a build uses whatever the registry serves at that moment:
a push to `main` changes every program built afterwards, and since the
compiler is itself packages, a bad push can break everyone. A lock file
pins a program to exact package versions:

```sh
packenger lock app.pkg     # writes app.lock next to app.pkg
packenger run app.pkg      # uses app.lock from now on
```

```
// Packenger lock file for app.pkg, written by 'packenger lock app.pkg'.
// Builds fetch exactly these package versions. Don't edit it: run
// 'packenger lock' again to update it.
registry https://raw.githubusercontent.com/DominicThemiya/Packenger/edb78a3e188290b5edf06e406c0e58451f51e737/registry
bintables b9731a375e8113cdbd1a476f2c0891a4f3d713b428c522031978fe918ad3391d
bintables/add dd39c7c9960213d9d3054d373579a7afbe90f8d6061888a50ac7cefaeb3626be
...
```

- **The registry is pinned.** For a GitHub raw URL on a branch or tag
  (such as the default, `.../main/registry`), `lock` asks the GitHub API
  which commit that is now, and records the URL of that commit. Files at a
  commit never change, so locked builds keep getting the same packages
  even after new pushes. Other registries are recorded as they are.
- **Every package is hashed.** Each package fetched is checked against its
  SHA-256 ([rt/sha256](registry/rt/sha256.md) in `pkc`). A package that
  differs, or one that isn't in the lock, stops the build with a message
  saying to run `packenger lock` again.
- **What is locked:** every front-end package the program loads, and the
  *whole* backend truck, so editing the program to use another construct
  doesn't need a new lock. Changing the `use` lines does.
- **The package tree's routes** are pinned the same way, one
  `route <prefix> <registry>` line each, and builds use them instead of
  `pkg_tree.pkg`.
- **Which registry is used:** `--registry` if given, else the lock's, else
  the package tree's `*`, else `PACKENGER_REGISTRY`, else the default. With `--registry` the hashes are
  still checked, so a lock also catches a mirror serving different files.

To update to the newest packages, run `packenger lock` again. It ignores
the old lock, and `-vb` shows every package it fetches. Commit lock files
with the program they belong to.

## Adding a package

1. Write `registry/<truck>/<name>.pkg`, with a `doc` section (see
   [packages and trucks](packages.md)).
2. Add `<name>` to the truck's member list in `registry/<truck>.pkg`.
3. If it is a syntax package without `expand`, add a backend package with
   the same name, `registry/bintables/<name>.pkg`, and list it in
   `registry/bintables.pkg`. Do the same in `ctables` (C) and `webtables`
   (WebAssembly); the harness runs every test program with all three.
4. Try it with the local registry, then regenerate the docs and run the
   tests:

   ```sh
   PACKENGER_REGISTRY=file://$PWD/registry ./bin/packenger run test.pkg -vb
   go run ./cmd/pkgdoc
   go test ./...
   ```

5. Commit and push. GitHub's raw file cache can take up to about five minutes
   to serve the new version.

A new truck is the same: a `<truck>.pkg` file with a `doc` and a `truck`
section, plus a folder for its members.

A library package (a `code` section) needs no backend. Give it tests as
Packenger programs using [rt/test](registry/rt/test.md) in `tests/<truck>/`,
ending with `return test_done()`; `go test ./...` runs `tests/rt` and
requires each to print `PASS`.

## Documentation

The [package reference](registry/README.md) in `docs/registry/` is
generated. Never edit it by hand; edit the package's `doc` section and run:

```sh
go run ./cmd/pkgdoc
```

The generator also checks the registry and refuses to write if:

- a package has no `doc` summary;
- a truck lists a member that doesn't exist;
- a package isn't listed in its truck;
- a package `needs` a package that doesn't exist.

`go test ./...` fails if any of those checks fail, if `docs/registry/` is
out of date, or if a generated link is broken.

### What a good package doc contains

- **Summary**: what it is and how it's written, e.g.
  `Conditional: if cond ... [else ...] end`.
- **Body**: what it does, a short example, and its edge cases and limits.
  For a backend package: which registers and stack slots it uses, and why
  each `emit` variant exists.

The generator adds the rest from the package itself: its syntax, a table of
its slots, its expansion or NASM variants, what it needs and what uses it,
and the link between each syntax package and its backend package.

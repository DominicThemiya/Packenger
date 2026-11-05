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
or `PACKENGER_REGISTRY`. For working on packages locally use a `file://`
URL:

```sh
export PACKENGER_REGISTRY=file://$PWD/registry
```

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
- **Which registry is used:** `--registry` if given, else the lock's, else
  `PACKENGER_REGISTRY`, else the default. With `--registry` the hashes are
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
   `registry/bintables.pkg`. Do the same in `ctables`, the C backend; the
   harness runs every test program with both.
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

# Publishing packages

## The registry

The registry is a folder of package files served over HTTP. By default it is
the [`registry/`](../registry) folder of this repository, served by GitHub:

```
https://raw.githubusercontent.com/DominicThemiya/Packenger/main/registry
```

Package `std/if` is fetched from `<registry>/std/if.pkg`, and a truck `std`
from `<registry>/std.pkg`. Packages are downloaded at compile time and kept
in memory only; nothing is cached on disk.

Any static file host works. Point the compiler at it with `--registry URL`
or `PACKENGER_REGISTRY`. For working on packages locally use a `file://`
URL:

```sh
export PACKENGER_REGISTRY=file://$PWD/registry
```

## Adding a package

1. Write `registry/<truck>/<name>.pkg`, with a `doc` section (see
   [packages and trucks](packages.md)).
2. Add `<name>` to the truck's member list in `registry/<truck>.pkg`.
3. If it is a syntax package without `expand`, add a backend package with
   the same name, `registry/bintables/<name>.pkg`, and list it in
   `registry/bintables.pkg`.
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

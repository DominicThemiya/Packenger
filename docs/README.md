# Packenger documentation

| guide | what it covers |
|---|---|
| [Getting started](getting-started.md) | install, build the compiler, compile and run a program |
| [The std language](language.md) | writing programs with the standard truck |
| [Packages and trucks](packages.md) | the package file format: syntax, expansion, NASM templates, docs |
| [Publishing packages](publishing.md) | the registry, adding packages and trucks, keeping docs current |
| [Command line](cli.md) | every command and flag, including the `-vb` build report |
| [How the compiler works](architecture.md) | the pipeline, and where each part lives in the code |
| [Self-hosting plan](self-hosting.md) | roadmap for a Packenger compiler written in Packenger |
| [Dump formats](dumps.md) | `packenger dump` output and the comparison harness |
| [Runtime library](registry/rt.md) | the `rt` truck: memory, vectors, strings, maps, files, processes |
| [Standard extensions](registry/stdx.md) | the `stdx` truck: `+=` and friends, `for x in xs`, `match`, `assert` |
| [Package reference](registry/README.md) | generated: every truck and package in the registry |

The package reference is generated from the packages themselves. Don't
edit `docs/registry/` by hand; see
[Publishing packages](publishing.md#documentation).

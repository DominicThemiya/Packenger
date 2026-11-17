# pkide, the Packenger IDE

`pkide` is an editor for Packenger programs and packages that runs as a
local web app. Completion, hover, go to definition and checking use the
compiler's own grammar loading and parser, so the suggestions are always
what the loaded packages define. Running, building, testing and formatting
use whichever Packenger build you choose.

```sh
go build -o bin/pkide ./cmd/pkide
./bin/pkide examples          # start it and open a window on examples/
```

## Installing it

`pkide` is a server on `127.0.0.1:6470` and a page it serves, so it
installs the way a web app does:

- **From the browser**: in Chrome, Edge or Brave, choose *Install pkide* in
  the address bar (or *Settings and install* in the app). It then opens in
  its own window from your app launcher.
- **From the terminal**: `pkide install` copies the binary to
  `~/.local/bin`, adds pkide to the app menu with its icon, and with
  `--service` adds a systemd user service that starts the server at login,
  so the installed app is always ready. `pkide uninstall` removes them.

If the server isn't running, the installed app says how to start it.
`pkide [folder]` starts the server if needed and opens a window, as an app
window in a Chromium browser if there is one.

## Looks

pkide uses [Catppuccin](https://catppuccin.com) Mocha, or Latte for a
light theme (*Settings*), with a darker, near-black background, square
corners throughout, and the
[Monaspace Krypton](https://monaspace.githubnext.com) font, falling back to
the system's monospace font if Krypton isn't installed. The editor keeps a
faint three-line banding and punched holes down the gutter, like
line-printer paper. The cursor is a `^` under the character it's at, gliding
from place to place, and the word at the cursor is highlighted everywhere
it occurs.

## The sidebar

The right sidebar (<kbd>Ctrl+I</kbd>, <kbd>Ctrl+Shift+B</kbd>, or the
buttons in the top bar) has two panels:

- **Suggestions** lists every suggestion for the cursor in full, grouped:
  what comes next in the statement, names in scope, fields, this file's
  definitions, statements and expressions, libraries, types and packages.
  Each shows its signature or pattern, its documentation and the package it
  comes from; the selected one (it follows the popup) also shows exactly
  what it inserts, placeholders marked. Above them is the pattern of the
  statement being written, or the signature of the call, with the current
  argument marked. Double-click or *Insert* to use one. At the top, *At the
  cursor* describes the word the cursor is on: a variable's type, a
  function's signature and docs, a keyword's patterns, or for a package on
  a `use` or `needs` line, its documentation, with a link to the
  definition.
- **Build** is the workspace's profile: the compiler (as cards, grouped
  into found, built from git and added), the target, and where packages
  come from; and building from git or adding compilers. The chip in the top
  bar shows the current build and target, and opens it.

## Writing code

| | |
|---|---|
| suggestions | appear as you type, or with <kbd>Ctrl+Space</kbd>; <kbd>Tab</kbd> or <kbd>Enter</kbd> accepts |
| snippets | statements come with their pattern filled in: `if ${cond}` … `end`; <kbd>Tab</kbd> moves to the next placeholder |
| hints | the status bar shows the pattern of the statement you're writing, or the signature of the function you're calling, with the current argument marked |
| hover | the type of a variable, a function's signature and doc comment, a keyword's patterns and package, a package's doc |
| definitions | <kbd>F12</kbd> or <kbd>Ctrl</kbd>+click; library functions and keywords open their package, read-only |
| problems | checked as you type, underlined, and listed under *Problems* |
| indentation | <kbd>Enter</kbd> after a line that opens a block indents and adds its `end`; `else`, `elif` and `end` move to their block's indentation |

What gets suggested depends on where the cursor is:

- **at the start of a line**: statements from the loaded packages, the
  words that can continue or close the block you're in (`elif`, `else`,
  `next`, `case`, `end`), and the variables, functions, globals and
  constants in scope;
- **in an expression**: names in scope, library functions (with their doc
  comments), and expression forms such as `len(…)` and `new T`;
- **after an operand**: what comes next in the statement (`to` after
  `for i = 0`, `in`, `->`) and word operators such as `as`;
- **after a dot**: the fields of the struct, worked out through variables,
  fields, indexing, calls and `as`;
- **after `:`, `->`, `as`, `new`**: types: primitives, structs, `ptr`, `[T]`
  and `fn(…) -> R`;
- **on a `use` or `needs` line**: packages from the registry, including the
  routes of a `pkg_tree.pkg`.

Package files get their own suggestions: section keywords with templates at
the start of a line; syntax kinds and flags on `syntax` lines; slot kinds
and type constraints inside `{…}` in patterns; the file's slot names,
`{each …}`, `@pos` and `$name` in `expand` templates; `@include`
completions in `emit` sections; the rule template and scopes in `resolve`;
member files in `truck`; and in `code`, everything a program gets, with the
grammar of the package's `needs`.

## Builds

The *Build* panel of the sidebar lists the Packenger compilers pkide knows, and the
workspace's profile: which build compiles it, for which target, and where
packages come from.

- **Found builds**: the repository's `bin/packenger`, `bin/pkc`,
  `bin/pkc-c` and `bin/pkc.wasm`, and `packenger` on `PATH`.
- **Added builds**: any compiler on disk.
- **Builds from git**: Packenger at any commit, branch or tag of the
  repository, built with `go build` into `~/.local/share/pkide/builds`,
  with `pkc` too if the commit has it. Each keeps the source it was built
  from, so its profile can use the registry of the same version.

`pkc.wasm` runs sandboxed in `wasmtime`, so it can check and print asm but
not run, build or test programs.

The registry of a profile is *default* (the program's lock file or package
tree, else `PACKENGER_REGISTRY`, else the universal tree), the repository's
`registry/` folder, the registry a git build came with, or any URL.
Completion and checking follow it too.

The top bar's *Run*, *Build*, *Test*, *Fmt*, *Why*, *Tree* and *Asm* run
the chosen build on the current file (saving first) and stream its output
to the console, where `file.pkg:line:col` positions are links. A running
program reads what you type in the console's input line. *Fmt* formats the
buffer with [`tools/pkfmt.pkg`](tools.md#pkfmt), built once per build.

## How it works

```
browser (cmd/pkide's page)          pkide server (127.0.0.1 only)
  editor, completion popup   ──►   internal/ide: Engine.Complete, Hover, Check,
  console (server-sent events)       Outline, Grammar  ── internal/lang.Analyze
  builds panel               ──►   Config: builds, profiles; Procs: runs
                                    the chosen build: packenger run|build|test|…
```

`lang.Compiler.Analyze` loads a file's grammar and parses it without
generating code. While you type, the engine analyzes the file without the
line being edited; if the file still doesn't parse (a block without its
`end` yet), it closes the open blocks at the cursor and tries again, and
otherwise uses the last version that parsed. A package file's code section
is analyzed as a program whose use lines are the package's `needs`.

The server only answers requests for `127.0.0.1`/`localhost` that carry the
session's token from its page, and only touches files inside the open
folder.

Settings and builds live in `~/.config/pkide` and `~/.local/share/pkide`.

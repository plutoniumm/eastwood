# Eastwood

A single-binary multi-language linter. Currently lints **Python**, **LaTeX**, **Go**, **JavaScript**, **TypeScript**, and **Rust** — 67 rules total. The binary is `eastwood`.

```sh
eastwood path/to/file.py
eastwood src/                    # walks a directory
cat foo.tex | eastwood --lang latex
eastwood --list-rules
```

## Install

### Homebrew (macOS / Linux)

```sh
brew tap plutoniumm/eastwood https://github.com/plutoniumm/eastwood
brew install eastwood
```

The tap points at this repo directly (URL form), so there's no separate `homebrew-eastwood` repo to maintain. The formula lives at [`Formula/eastwood.rb`](Formula/eastwood.rb) and pulls per-arch binaries from `manav.ch/eastwood/<version>/`.

### One-line installer

```sh
curl -fsSL https://manav.ch/eastwood/install.sh | bash
```

Drops `eastwood` in `/usr/local/bin`. Override with env vars:

```sh
EASTWOOD_VERSION=v0.1.0 \
EASTWOOD_INSTALL_DIR="$HOME/.local/bin" \
  curl -fsSL https://manav.ch/eastwood/install.sh | bash
```

### From source

```sh
go install github.com/plutoniumm/eastwood/cmd/eastwood@latest
```

Requires Go 1.25+ and a C toolchain (Xcode CLT on macOS). CGo is on for tree-sitter.

## Configuration

Eastwood reads `eastwood.toml` from the project root (walks up from CWD). The repo's own [`eastwood.toml`](eastwood.toml) is a working example with all the configurable knobs documented.

```toml
[linter]
fail_on = "warning"

[python.rules."py/line-too-long"]
max_length = 100

[latex.rules."tex/math-delimiters"]
prefer = "dollar"   # or "paren"

[python.rules."py/blank-before-return"]
enabled = true      # opt-in style preferences
```

`eastwood --list-rules` prints every rule with default severity. `eastwood --rule py/bare-except path/` runs a single rule.

## Releasing

Releases are built locally and uploaded to GitHub releases. Requires `zig` (for Linux cross-compilation) and `gh` (GitHub CLI).

```sh
brew install zig gh       # one-time
make release VERSION=0.1.0
```

This cross-compiles all four platform binaries, creates a GitHub release with the tarballs, patches the formula SHAs, commits, and pushes the tag.

For a "latest" pointer on the host, symlink `eastwood/latest -> v0.1.0` after each release; the install script defaults to `latest`.

## Architecture

See [`CLAUDE.md`](CLAUDE.md) for the full layout: `core/` contracts, per-language analyzer packages (`python/`, `latex/`, `golang/`, `javascript/`, `typescript/`, `rust/`, `svelte/`), runner, config, output, cache.

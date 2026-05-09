# Linter Design Document

A pluggable, single-binary linter for source code. v1 targets **Python** and **LaTeX**, with the architecture set up so additional languages (Go, JS, etc.) can be added later as analyzer modules.

## 1. Goals and Non-Goals

**Goals**

- One static Go binary; no runtime dependencies on Python, Node, or a TeX distribution.
- Per-language analyzers behind a shared rule/diagnostic framework.
- Manually authored rule sets per language — no attempt at cross-language rule sharing.
- Fast cold start, parallel file processing, deterministic output.
- Multiple output formats (text, JSON, SARIF) and autofix support.

**Non-Goals (v1)**

- A unified cross-language AST. Per-language analyzers stay per-language; only the framework is shared.
- Deep semantic analysis. Python rules are syntactic / tree-sitter-query based in v1; full scope/type analysis is deferred.
- LSP / editor integration. Designed to be added later, not v1.
- Replacing `ruff` or `chktex` as a daily driver. This is its own thing.

## 2. Host Language

**Go**, decided in prior discussion. Brief recap of the trade for this target set:

- Single static binary distribution. Ship one file, no interpreter required.
- Decent tree-sitter Go bindings; both Python and LaTeX have maintained tree-sitter grammars.
- Good concurrency primitives for parallel file processing.
- The original "go/types gives you free Go semantics" argument is *deferred* here, since Go isn't a v1 target. If priorities shifted to Python-first with deep semantic analysis, Rust (cf. `ruff`) would be a stronger pick. We accept the trade because (a) v1 rules don't need deep semantics and (b) keeping a stable host through future Go-target work is valuable.

## 3. Architecture Overview

```
                ┌──────────────────────────────┐
                │           CLI                │
                │  (cmd/linter/main.go)        │
                └──────────────┬───────────────┘
                               │
                ┌──────────────▼───────────────┐
                │          Runner              │
                │  - file discovery            │
                │  - parallel dispatch         │
                │  - diagnostic aggregation    │
                │  - output formatting         │
                └──────────────┬───────────────┘
                               │
              ┌────────────────┼────────────────┐
              │                │                │
        ┌─────▼─────┐    ┌─────▼─────┐    ┌─────▼─────┐
        │  Python   │    │  LaTeX    │    │  ... future│
        │ Analyzer  │    │ Analyzer  │    │           │
        └─────┬─────┘    └─────┬─────┘    └───────────┘
              │                │
        ┌─────▼─────┐    ┌─────▼─────┐
        │tree-sitter│    │tree-sitter│
        │  python   │    │   latex   │
        └───────────┘    └───────────┘
```

Per-language `Analyzer`s expose: a parser, a rule list, and (eventually, optionally) a semantic layer. The runner does not know about specific languages — it iterates `Analyzer`s registered at startup.

## 4. Project Layout

```
linter/
├── cmd/
│   └── linter/                  # main + flag parsing
│       └── main.go
├── internal/
│   ├── core/                    # Diagnostic, Rule, Severity, Position, Fix
│   ├── runner/                  # discovery, scheduling, aggregation
│   ├── config/                  # TOML loader, per-rule config, ignore globs
│   ├── output/                  # text, JSON, SARIF
│   └── tsutil/                  # tree-sitter helpers (queries, walks)
├── analyzers/
│   ├── python/
│   │   ├── analyzer.go          # registers rules, owns parser
│   │   ├── rules/
│   │   │   ├── mutable_default.go
│   │   │   ├── bare_except.go
│   │   │   └── ...
│   │   └── testdata/
│   └── latex/
│       ├── analyzer.go
│       ├── rules/
│       └── testdata/
├── pkg/api/                     # exported types if a library API is wanted
├── docs/
│   └── rules/                   # one markdown page per rule
├── go.mod
└── README.md
```

Rationale:

- `internal/` is private, not importable. `pkg/api/` exists only if you decide to publish a stable Go API.
- Each rule is its own file with a tight test fixture set in `testdata/`. Easier to review, easier to delete.
- One markdown file per rule under `docs/rules/<rule-id>.md`. The `linter explain <rule-id>` command reads these.

## 5. Core Types

```go
package core

type Severity int

const (
    Info Severity = iota
    Warning
    Error
)

type Position struct {
    File   string
    Line   int // 1-indexed
    Column int // 1-indexed (in runes, not bytes)
    Offset int // byte offset, for editing
}

type Range struct {
    Start, End Position
}

type TextEdit struct {
    Range   Range
    NewText string
}

type Fix struct {
    Description string
    Edits       []TextEdit
}

type Diagnostic struct {
    RuleID   string
    Severity Severity
    Message  string
    Range    Range
    Fixes    []Fix // zero or more; runner picks first non-conflicting
}

type RunContext struct {
    File     *SourceFile
    Source   []byte
    Tree     *sitter.Tree // tree-sitter parse tree
    Language string
    Config   *RuleConfig
    Report   func(Diagnostic) // rules call this; cleaner than returning slices
}

type Rule interface {
    ID() string                // e.g. "py/mutable-default-arg"
    Description() string       // one-line summary
    DefaultSeverity() Severity
    Check(ctx *RunContext)
}

type Analyzer interface {
    Language() string                 // "python", "latex"
    Extensions() []string             // [".py"], [".tex", ".cls", ".sty"]
    Parse(src []byte) (*sitter.Tree, error)
    Rules() []Rule
}
```

Notes:

- Rules `Report()` diagnostics through a callback rather than returning a slice; lets the runner stream/limit/dedupe and keeps rule code tidy.
- Rule IDs are namespaced by language prefix (`py/`, `tex/`). Avoids collisions and makes config explicit.
- `Range` is byte-offset based for correctness with multibyte source; `Column` is the rune column for human display.

## 6. Parsing Layer

**Library:** `github.com/tree-sitter/go-tree-sitter` (official binding) plus per-language grammar modules. Pin a specific version of grammars at module level; tree-sitter ABI bumps are real and break things.

**Concerns to resolve early:**

- CGo dependency: cross-compilation needs care. Document supported targets up front (linux/amd64, linux/arm64, darwin/arm64, windows/amd64).
- Grammar versioning: tree-sitter-python evolves. Pin and re-test on bump.
- Performance: parse + cache. Cache key = `(file_path, content_hash, grammar_version, ruleset_hash)`. Stored under `~/.cache/linter/` with simple file layout.

**Helper API (`internal/tsutil`):**

```go
type Cursor struct{ /* wraps sitter.TreeCursor with ergonomic walks */ }

// Run a query and yield captures.
func Query(tree *sitter.Tree, src []byte, query string) iter.Seq[Capture]

// Convert tree-sitter Point + byte offset to core.Position.
func PointToPosition(p sitter.Point, src []byte, file string) core.Position
```

Goal: rule authors should rarely touch raw tree-sitter primitives.

## 7. Python Analyzer

### 7.1 Parser

`tree-sitter-python` grammar. Mature, well-maintained.

### 7.2 Semantic layer (deferred)

v1 has **no scope analysis**. Rules that need "is this name used / defined / shadowed" are out of scope. When that becomes a blocker, build `internal/python/scope` on top of the parse tree — basically a symbol table walker. Estimate: a couple of weeks of focused work for a usable scope/import resolver. Defer until at least 3 rules genuinely need it.

### 7.3 Initial rule set (v1)

All achievable with tree-sitter queries plus light logic:

| Rule ID                       | What it flags                                                  | Fixable |
|-------------------------------|----------------------------------------------------------------|---------|
| `py/mutable-default-arg`      | `def f(x=[])`, `def f(x={})`                                  | no      |
| `py/bare-except`              | `except:` without exception type                              | no      |
| `py/comparison-to-none`       | `x == None` instead of `x is None`                            | yes     |
| `py/comparison-to-bool`       | `x == True`, `x == False`                                     | yes     |
| `py/f-string-no-placeholder`  | `f"hello"` with no `{...}`                                    | yes     |
| `py/print-statement`          | bare `print()` calls (configurable; off by default)           | no      |
| `py/empty-docstring`          | `def`/`class` whose docstring is empty or whitespace          | no      |
| `py/percent-format`           | `"%s" % x` formatting (configurable)                          | no      |
| `py/redundant-parens-return`  | `return (x)` where parens add nothing                         | yes     |
| `py/assert-tuple`             | `assert (x, y)` (always truthy — common bug)                  | no      |

These are deliberately the kind of pattern-shaped rules where tree-sitter shines. Anything requiring "is this name bound" or "what type is this" waits for the scope layer.

### 7.4 Example rule implementation

```go
// analyzers/python/rules/mutable_default.go
package rules

const mutableDefaultQuery = `
(default_parameter
  value: [(list) (dictionary) (set)] @bad)
`

type MutableDefaultArg struct{}

func (MutableDefaultArg) ID() string                 { return "py/mutable-default-arg" }
func (MutableDefaultArg) Description() string        { return "mutable default argument" }
func (MutableDefaultArg) DefaultSeverity() core.Severity { return core.Warning }

func (r MutableDefaultArg) Check(ctx *core.RunContext) {
    for cap := range tsutil.Query(ctx.Tree, ctx.Source, mutableDefaultQuery) {
        ctx.Report(core.Diagnostic{
            RuleID:   r.ID(),
            Severity: r.DefaultSeverity(),
            Message:  "mutable default argument; use None and assign inside",
            Range:    tsutil.NodeRange(cap.Node, ctx.Source, ctx.File.Path),
        })
    }
}
```

## 8. LaTeX Analyzer

### 8.1 Parser

`tree-sitter-latex`. Be honest about its limits up front:

- It parses surface structure: environments, commands, math regions, comments.
- It does **not** expand macros. `\newcommand{\foo}{...}` is a node, but `\foo` is not what `\foo` expands to. Any rule that depends on macro semantics is out of reach without integrating with a TeX engine, which we are not doing.
- Many real-world `.tex` files use package-specific commands the grammar doesn't recognize; these often parse as `generic_command` nodes. Rules need to handle that gracefully.

### 8.2 Two-track rules

LaTeX rules split into:

1. **Tree-sitter-driven** — structural rules (mismatched environments, `$$...$$` vs `\[...\]`, math-mode misuse).
2. **Regex/text-driven** — typographic rules where the grammar isn't helpful (non-breaking-space conventions, smart-quote misuse). These run over the source after a comment-stripping pass.

The `Rule` interface accepts both kinds; track 2 rules just ignore `ctx.Tree` and read `ctx.Source`.

### 8.3 Initial rule set (v1)

| Rule ID                          | What it flags                                                       | Fixable |
|----------------------------------|---------------------------------------------------------------------|---------|
| `tex/double-dollar-display-math` | `$$...$$` (use `\[...\]`)                                          | yes     |
| `tex/missing-nbsp-before-cite`   | `Smith \cite{...}` (should be `Smith~\cite{...}`)                  | yes     |
| `tex/missing-nbsp-after-fig`     | `Fig. 1` (should be `Fig.~1`); same for Eq., Sec., Tab., Alg.      | yes     |
| `tex/straight-quotes`            | `"..."` (should be `` `` ... '' ``)                                | yes     |
| `tex/multiple-blank-lines`       | 3+ consecutive blank lines                                          | yes     |
| `tex/mismatched-environment`     | `\begin{X}` without matching `\end{X}` (tree-sitter handles this)  | no      |
| `tex/space-before-punctuation`   | ` ,` ` .` ` ;` (configurable; French typography flips this)        | yes     |
| `tex/empty-section`              | `\section{...}` followed immediately by another section            | no      |
| `tex/wide-hyphen-in-range`       | `pages 10-20` (should be `10--20`) — heuristic, opt-in             | yes     |
| `tex/inconsistent-math-delim`    | mix of `$...$` and `\(...\)` in same file                          | no      |

`chktex` is mature prior art. Read its rule list for inspiration; it is BSD-licensed and the rules themselves are not copyrightable, but code should be original.

## 9. Configuration

### 9.1 File

`.linter.toml` at project root. Discovered by walking up from CWD or each input path.

```toml
[linter]
include = ["**/*.py", "**/*.tex"]
exclude = ["**/vendor/**", "**/_build/**"]
fail_on = "warning"  # info | warning | error

[python]
enable = ["py/*"]
disable = ["py/print-statement"]

[python.rules."py/percent-format"]
severity = "info"

[latex]
enable = ["tex/*"]
disable = []

[latex.rules."tex/space-before-punctuation"]
# French typography: spaces before : ; ! ? are correct.
allow_before = [":", ";", "!", "?"]
```

### 9.2 Inline directives

- Python: `# linter: disable=py/bare-except` (line-scoped). `# linter: disable-next=...` for next line. `# linter: disable-file=...` at top of file.
- LaTeX: same with `%` instead of `#`.

Inline directives parse via a regex pass during file load, independent of grammar. Stored on the `SourceFile` and consulted by the runner before emitting each diagnostic.

## 10. Output

### 10.1 Formats

- **Text** (default): GCC-style, `path:line:col: severity: message [rule-id]`. Color when stdout is a TTY.
- **JSON**: array of diagnostics, schema versioned in the top-level object.
- **SARIF 2.1.0**: for GitHub code scanning, GitLab, etc.

Selected via `--format text|json|sarif`. JSON and SARIF are stable contracts; text is for humans and may change.

### 10.2 Exit codes

- `0` — no diagnostics at or above `fail_on` severity.
- `1` — diagnostics found at or above `fail_on`.
- `2` — internal error (parse failure that wasn't recoverable, config error, IO error).

Parse errors for individual files are emitted as `lint/parse-error` diagnostics, not exit-2. That keeps "lint a directory of broken files" usable.

## 11. Autofix

- Each `Diagnostic` may carry zero or more `Fix` candidates.
- Each `Fix` is a list of `TextEdit`s with byte-offset ranges.
- `--fix` applies fixes that:
  1. Don't conflict with another applied fix in the same file.
  2. Are idempotent (applying twice doesn't break things).
- Conflict resolution: greedy by source position; first wins, conflicting fixes deferred to a second pass after re-parsing.
- `--fix-dry-run` prints unified diffs without writing.
- `--fix-only-rule <rule-id>` to run a single rule's fixes.

Multi-pass: after a pass of fixes, re-run rules; some fixes expose others. Cap at 3 passes to avoid pathological loops.

## 12. CLI Surface

```
linter check [PATH...]              # default action; lints paths or CWD
linter check --fix
linter check --fix-dry-run
linter check --format json
linter check --rule py/bare-except  # restrict to one rule (for development)

linter rules [--language python]    # list available rules
linter explain RULE_ID              # show rule docs

linter init                         # write default .linter.toml
linter version
```

Hidden / development:

```
linter dev parse FILE               # dump tree-sitter tree
linter dev query FILE QUERY         # run a tree-sitter query against a file
```

The `dev` subcommands are essential for rule authoring and worth shipping.

## 13. Runner Behavior

- Walk paths, apply include/exclude globs, dispatch each file to the matching analyzer by extension.
- Worker pool sized to `runtime.NumCPU()` by default; `--jobs` override.
- Each worker: parse → run all enabled rules → collect diagnostics → filter by inline directives → push to aggregator.
- Aggregator: collects, sorts (by file, line, column, rule ID), deduplicates, formats.
- Caching: skip work when `(file_hash, ruleset_hash, grammar_version)` matches a cached result.

## 14. Testing Strategy

### 14.1 Rule tests

Each rule has a `testdata/` directory with paired files:

```
testdata/
├── mutable_default_arg/
│   ├── basic.py
│   ├── basic.py.expected   # expected diagnostics, one per line
│   └── ok.py               # negative case, expected empty
```

A single test runner walks all `testdata/` dirs and asserts the diagnostic set matches. Regenerate goldens with `go test ./... -update`.

### 14.2 Integration tests

Run the CLI against a corpus of real Python and LaTeX projects (vendored in a separate `corpus/` repo, or as git submodules). Snapshot diagnostic counts per project; alert on large deltas.

### 14.3 Fuzzing

`go test -fuzz` against the parser wrappers. Tree-sitter is robust but our query helpers are not; fuzz them.

## 15. Milestones

- **M0 — Skeleton.** Go module, CLI flags, tree-sitter integrated, one Python rule (`mutable-default-arg`) firing end-to-end. Text output only. ~1 week.
- **M1 — Framework.** Diagnostic types, Rule/Analyzer interfaces, runner with parallelism, file discovery, inline directives. ~1 week.
- **M2 — Python rule pack.** All 10 rules from §7.3. Per-rule docs. ~2 weeks.
- **M3 — LaTeX rule pack.** All 10 rules from §8.3. ~2 weeks.
- **M4 — Config.** TOML loader, per-rule config, include/exclude, severity overrides. ~3 days.
- **M5 — Output formats.** JSON + SARIF + colorized text. ~3 days.
- **M6 — Autofix.** Fix infrastructure, mark fixable rules, multi-pass runner. ~1 week.
- **M7 — Caching.** On-disk result cache. ~3 days.
- **M8 — (optional) LSP.** Language server exposing diagnostics to editors.
- **M9 — (optional) Python scope analyzer.** Unlocks `unused-import`, `unused-variable`, `redefined-name`. ~2 weeks.

Total to a usable v1 (M0–M7): roughly 6–8 weeks of focused work.

## 16. Risks and Open Questions

1. **tree-sitter-latex grammar gaps.** Real `.tex` files in the wild use packages and macros the grammar doesn't model. Mitigation: text-track rules for things the grammar can't reach; test corpus drawn from real papers, not minimal examples.
2. **Macro expansion is impossible.** Any rule premised on knowing what `\foo` expands to cannot work with this architecture. Be explicit in docs about what kinds of rules are out of scope.
3. **Python without scope analysis is limited.** Most "real" Python lint value is in scope/type. Plan M9 from the start; design `RunContext` so the future scope info plugs in cleanly without rewriting rules. Suggested: `ctx.Symbols` field, nil in v1, populated later.
4. **CGo and cross-compilation.** Document supported targets early. If a pure-Go tree-sitter alternative matures during the project, evaluate it.
5. **Rule ID stability.** Rule IDs end up in users' config files and inline directives. Once shipped, they cannot be renamed without a deprecation cycle. Pick names carefully on first publish.
6. **Diagnostic ranges from tree-sitter.** Tree-sitter reports byte offsets and (row, column) where column is UTF-16 code units in some bindings, bytes in others. Verify exactly what `go-tree-sitter` returns and convert consistently to rune columns for display.
7. **Performance baseline.** Set a target early (e.g., "1 MB of Python in <100 ms cold, <20 ms warm"). Without a target, performance regressions go unnoticed.

## 17. Out of Scope (for now)

- IDE/editor integration (LSP) — M8 stretch.
- Cross-language rule abstraction — explicitly rejected, see §1.
- Custom user rules in a scripting language — interesting but a large project of its own.
- Type-aware Python analysis beyond what a hand-rolled scope analyzer can give — would require integrating something like a Pyright sidecar, which conflicts with the single-binary goal.

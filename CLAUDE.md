# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Eastwood (binary name `le`) is a single-binary multi-language linter, written in Go (`go 1.25.5`, module `eastwood`). It currently lints **Python**, **LaTeX**, **Go**, **JavaScript**, **TypeScript**, and **Rust** — 67 rules total. It uses tree-sitter via `github.com/smacker/go-tree-sitter` for the AST-based languages and BurntSushi/toml for config. LaTeX is regex/text-driven (tree-sitter-latex is not integrated).

## Common commands

```bash
go build ./cmd/le              # produce ./le
go run ./cmd/le [paths...]     # lint paths (or CWD if none)
go run ./cmd/le --list-rules
go run ./cmd/le --rule py/bare-except path/         # restrict to one rule
cat foo.tex | go run ./cmd/le --lang latex          # stdin mode (--lang required, or detect/FromContent guesses)
go test ./...                  # no tests are checked in yet, but this is the convention to use
go vet ./...
```

CLI flags actually implemented: `--lang`, `--format text|json`, `--jobs`, `--config`, `--rule`, `--list-rules`, `--version`. There are **no** `check`/`explain`/`rules`/`init`/`dev` subcommands — `le` is single-purpose; ignore the subcommand surface in `design.md`.

`eastwood.toml` is **required at runtime**: `config.Chain` walks up from CWD and returns an error if no config file is found anywhere in the chain. When developing in a fresh checkout, create one (even empty `[linter]` section works) before running `le` against real files.

## design.md vs reality

`design.md` is the original design document and is partly **aspirational**. When it disagrees with the code, the code wins. Concretely, the following are **not implemented** despite appearing in the design doc:

- Autofix (`Fix`/`TextEdit`/`--fix`/`--fix-dry-run`) — `core.Diagnostic` has no `Fixes` field.
- SARIF output — only `text` and `json` (NDJSON, one object per line) exist.
- On-disk result caching.
- Python scope/symbol analysis.
- `tree-sitter-latex` integration — `latex.Analyzer.Parse` returns `(nil, nil)`; LaTeX rules are 100% regex/text-driven and use `latexCommentRanges` (a hand-rolled `%`-to-EOL scanner that respects `\%`).
- The `internal/` + `analyzers/<lang>/rules/<rule>.go` directory layout. Real layout is flat: each language is one package with all its rules in a single file (`python/python.go`, `latex/latex.go`).

What *is* implemented matches design intent: per-language analyzers behind `core.Analyzer`, parallel runner, inline `eastwood:` directives, language detection, severity-based exit codes.

## Architecture

The pipeline is `cmd/le/main.go` → `runner.Run` → per-file `Analyzer.Parse` + each `Rule.Check` → diagnostics → `output.Formatter`.

**`core/`** — the contracts. `Analyzer` (Language, Extensions, Parse, **CommentRanges**, Rules), `Rule` (ID, Description, DefaultSeverity, Check), `RunContext` (File, Tree, RuleConfigs, **CommentRanges**, Report callback), `Diagnostic`, `Position` (1-indexed line, **rune column**, byte offset), `ByteRange` (half-open).

**`runner/`** — discovers files (walks dirs, skips dotted dirs, matches by extension via `analyzer.Extensions()`), builds a fixed-size worker pool (`--jobs`, default `NumCPU`), and for each file: calls `Parse`, gets `CommentRanges`, parses inline directives, then runs every enabled rule. The `Report` callback applies inline-directive suppression but **does not** auto-suppress diagnostics that fall in comment ranges — rules that scan code text filter comments themselves (text-track LaTeX rules use `findAll(re, src, ctx.CommentRanges)`; tree-sitter queries on Python don't match inside `(comment)` nodes). Rules that *target* comments (e.g. `py/divider-comment`, `py/type-ignore`, `py/file-leading-comment`) emit freely. Diagnostics are sorted by (line, col, ruleID) and streamed to the formatter as each file finishes. Exit code is 1 iff `maxSeverity >= cfg.FailOn` (default `warning`), else 0; 2 is reserved for setup errors.

**Inline directives** (parsed by `runner.parseDirectives`, regex `eastwood:\s*(disable|disable-next|disable-file)=([^\s]+)`): only matched **inside comment ranges**, so e.g. a Python `# eastwood: disable=py/foo` works but a string literal containing the same text does not. `*` is a wildcard rule ID. Comma-separated rule lists are supported.

**`config/`** — TOML chain loader. Walks from CWD up to `/`, collecting every `eastwood.toml`, then merges outermost→innermost (child wins). `IsEnabled` honors `enable`/`disable` glob lists per language (single `*` wildcard). Per-rule config lives under `[<lang>.rules."<rule-id>"]` and is plumbed to rules via `ctx.RuleConfig(id)` (returns `RuleConfig{}`, never nil).

**Per-language packages** — each implements `core.Analyzer`. `python/`, `latex/`, `golang/` (so named because `go` is a Go keyword), `javascript/`, `typescript/`, `rust/`. The pattern in each is: an `Analyzer` struct with `Language()`, `Extensions()`, `Parse()`, `CommentRanges()`, `Rules()`. Each package holds a package-level `lang = <ts>.GetLanguage()` for queries.

To add a new language: create `<lang>/<lang>.go`, register the analyzer in `cmd/le/main.go` `allAnalyzers`, add the extensions to **three** maps (`detect.extMap`, `runner.extToLang`, and the implicit `Extensions()` method), add a TOML section + `ResolvedLang` field to `config/config.go` (in both `File` and `Config`, plus `merge`), and add the case to `runner.langConfig`. The four maps stay in lockstep; if you add a language and forget one, files of that extension either won't be discovered or won't be configurable.

**`langutil/`** — shared helpers and rule factories used across language packages. Two files:

- `langutil/util.go`: `LineInfo` (line-start cache with O(log n) `PositionAt`/`LineIndex`/`LineSlice`), `ReportAt(ctx, ruleID, msg, sev, startOff, endOff)` (one-shot diagnostic emission — builds LineInfo internally; for hot paths, build LineInfo once and use directly), `InAnyRange(offset, ranges)`, `HasInlineCComment(s)` (skip empty-block diagnostics on `{ /* TODO */ }` blocks).
- `langutil/rules.go`: cross-language rule **factories** that take an ID prefix and a `*sitter.Language` and return a `core.Rule`. `TodoCommentRule(prefix, lang, commentTypes...)` (variadic on comment types — pass `"line_comment", "block_comment"` for Rust), `EmptyBlockRule(prefix, lang, blockType)`, `TripleEqualsRule(prefix, lang)`, `NoVarRule(prefix, lang)`, `NoDebuggerRule(prefix, lang)`. Each factory carries the rule logic; per-language packages register the factory output (e.g., `langutil.TodoCommentRule("go", lang)`) instead of duplicating struct definitions. The pattern shrinks each new-language file by ~50%.

**`tsutil.CommentRangesFromTree` is variadic** — pass one type for grammars with a single `comment` node (Python, JS, TS, Go), or multiple for grammars that distinguish kinds (Rust uses both `line_comment` and `block_comment`). `tsutil.NodeText(node, src)` is the canonical "extract source text for a node" helper used across packages.

**`python/`** — tree-sitter-driven. Rule IDs are `py/...`. Comment node type is `"comment"`. **The package is split across three files**:

- `python.go` (~110 lines): `Analyzer` + `Rules()` registration + the shared helpers `nodeText`/`nodeRange` (thin wrappers over `tsutil`) + `findCompOp` (used by comparison-to-{none,bool}, not-in, and type-comparison) + the cross-file query constants `commentQuery`, `assertStmtQuery`, `dictionaryQuery`.
- `rules_core.go` (~540 lines): the original 10 design.md rules plus the 7 default-on qudit-style rules (assert-no-message, divider-comment, semicolon-separator, type-ignore, file-leading-comment, inline-dict, trailing-comma-dict). Local helpers `hasChildType`, `stripStringDelimiters`, `hasRepeatedRun`, `countDictKeys` live here.
- `rules_ruff.go` (~590 lines): the 3 opt-in qudit blank-line rules (`py/blank-{before-return,after-return,before-assert}`) plus the 12 ruff-derived rules. Local helper `hasBlankSeparator` lives here.

Rules split into two camps:

- **Code rules** (`py/mutable-default-arg`, `py/bare-except`, `py/comparison-to-*`, `py/f-string-no-placeholder`, `py/print-statement`, `py/empty-docstring`, `py/percent-format`, `py/redundant-parens-return`, `py/assert-tuple`, `py/assert-no-message`, `py/inline-dict`, `py/trailing-comma-dict`, `py/assert-false`, `py/super-with-args`) — operate on AST nodes from tree-sitter queries.
- **Comment / text rules** (`py/divider-comment`, `py/semicolon-separator`, `py/type-ignore`, `py/file-leading-comment`, `py/line-too-long`, `py/trailing-whitespace`, `py/no-newline-at-eof`, `py/blank-lines-at-eof`) — query `(comment)` or `(string)` nodes, or scan source text directly. The semicolon rule skips `;` inside string literals by collecting `(string) @s` byte ranges first.
- **Opt-in preference rules** (`py/blank-before-return`, `py/blank-after-return`, `py/blank-before-assert`) — default off; user enables per-rule via `[python.rules."py/blank-before-return"] enabled = true`. The rule reads `ctx.RuleConfig(r.ID()).Bool("enabled", false)` and bails early if false. This pattern (rule self-checks an `enabled` flag) is how eastwood expresses opt-in preferences; the standard `enable`/`disable` glob lists in `[python]` continue to work for default-on rules.

Style rules ported from `plutoniumm/qudit/view/lint_checks.py`: `py/divider-comment` (5+ repeated chars in a comment, manual run-counter since Go regexp has no backreferences), `py/semicolon-separator`, `py/type-ignore`, `py/file-leading-comment` (file should open with imports, not comments/docstrings; shebangs OK), `py/inline-dict`, `py/trailing-comma-dict` (checks `child[count-2].Type() == ","` since tree-sitter preserves anonymous tokens in `Child(i)` order), `py/assert-no-message`, plus the three opt-in blank-line preference rules. Ruff-style additions: `py/line-too-long` (configurable `max_length`, default 88, counted in runes via `utf8.RuneCount`), `py/trailing-whitespace`, `py/no-newline-at-eof`, `py/blank-lines-at-eof`, `py/assert-false` (B011), `py/super-with-args` (UP008, flags `super(Cls, self)` with exactly 2 args). Skipped from qudit as too subjective: ≤2-word variable-name limit, docstring-escape rules.

`py/super-with-args` shows the pattern for queries that need correlated child-node info: capture the parent node (`(call) @c`) once, then walk fields with `node.ChildByFieldName("function")` and `node.ChildByFieldName("arguments")` to inspect siblings — `tsutil.Query` flattens captures across matches, so multi-capture queries lose match correlation.

**`golang/` `javascript/` `typescript/` `rust/`** — sensible-defaults rule sets for each language, all tree-sitter-driven. **Most rules come from `langutil` factories**; only language-specific rules live as locally-defined structs.

- `golang/` (3 rules): `langutil.TodoCommentRule("go", lang)`, `langutil.EmptyBlockRule("go", lang, "block")`, plus locally-defined `go/explicit-true-comparison` (`x == true` → `x`).
- `javascript/` (4 rules, all from factories): `langutil.NoVarRule("js", lang)`, `langutil.TripleEqualsRule("js", lang)`, `langutil.NoDebuggerRule("js", lang)`, `langutil.TodoCommentRule("js", lang)`. The package file is ~50 lines — the analyzer scaffolding.
- `typescript/` (5 rules): four factory-shared rules with `ts/` prefix + locally-defined `ts/no-any` (queries `(predefined_type) @t` filtered to text `"any"`).
- `rust/` (3 rules): `langutil.TodoCommentRule("rust", lang, "line_comment", "block_comment")` (variadic comment types), `langutil.EmptyBlockRule("rust", lang, "block")`, plus locally-defined `rust/dbg-macro` (queries `(macro_invocation macro: (identifier) @name)` filtered for `dbg`).

Adding a new rule that's already in the factory set: one line in `Rules()`. Adding a language-specific rule: define a struct in the package file and add it to `Rules()`.

**`latex/`** — text/regex driven (no tree-sitter). Local helpers: `buildLineInfo` (rebuild per call, no caching — kept rather than aliasing to `langutil.LineInfo` because dozens of call sites use lowercase `positionAt`/`lineIndex`/`lineSlice` method names), `latexCommentRanges` (handles `\%` escape), `findAll` (regex matches that auto-skip comments), `reportAt` (thin wrapper for offset → line/col diagnostic), `inComment`. The package is split across two files:

- `latex.go` (~810 lines): `Analyzer`, helpers, and the 16 text-style/structural/cross-ref rules.
- `rules_widthsanity.go` (~190 lines): the 4 REVTeX two-column overflow heuristics (wide-table, wide-figure, wide-includegraphics, long-equation), plus their dedicated regexes (`tableEnvRe`, `figureEnvRe`, etc.) and helpers `countTabularCols` / `sumColumnwidthFractions`. `figureEnvRe` is shared between two of these rules so they live together.

Rule IDs are `tex/...`. Rule semantics target REVTeX two-column paper style:

- `tex/math-delimiters` is direction-configurable. `prefer = "dollar"` (default) flags `\(...\)` and `\[...\]`; `prefer = "paren"` flags `$...$` and `$$...$$` (escape-aware: skips `\$`). Both modes emit one diagnostic per delimiter token — opener and closer both get flagged because both need to change.
- `tex/dashes` is mode-configurable. `prefer = "no-em-en"` (default) flags Unicode em/en dashes and ASCII `--` / `---` *outside* TikZ/qcircuit lines. `prefer = "endash-ranges"` flags single hyphens between digit runs (e.g. `pages 10-20` → suggest `10--20`); skips ranges that already use `--`. `prefer = "both"` runs both checks. The TikZ-line skip is heuristic: if the line (after leading whitespace) starts with `%` or one of `\draw \path \node \fill \filldraw \foreach \multigate \ghost \ctrl \targ \meter \qw \rstick \lstick \gate \Qcircuit \begin{tikzpicture} \end{tikzpicture}`, dashes on it are ignored.
- `tex/forbidden-pattern` is fully driven by config — `[latex.rules."tex/forbidden-pattern"] patterns = [...]`, each entry a Go regexp. Use TOML literal strings (single-quoted) to avoid backslash hell. Invalid regexes produce an `error`-severity diagnostic at offset 0.
- `tex/uncited-label` only checks labels in the `tab:` / `fig:` / `eq:` namespaces. References in `\cref{a,b,c}` are split on commas.
- `tex/missing-graphic` does an `os.Stat` on the path relative to the source file's directory, trying `.pdf`, `.png`, `.jpg`, `.jpeg`, `.eps` suffixes if the bare path doesn't exist. Skipped when the file is `<stdin>`.
- The four `tex/wide-*` and `tex/long-equation` rules are info-severity heuristics for REVTeX two-column overflow risk; they're not precise.

The `mismatched-environment` rule is the most involved — sorts begin/end events by offset and runs a stack matcher.

**`tsutil/`** — wraps `go-tree-sitter`. `Query` panics on invalid query strings (treated as programmer error — fine for compile-time-known queries). `pointToPos` converts tree-sitter's byte column to a **rune column** for human-readable display, while keeping the byte offset around for editing.

**`detect/`** — extension map first (`FromPath`); content-based scoring fallback (`FromContent`, used for stdin without `--lang`) with a small-but-fine marker list and a confidence threshold.

## Adding a rule

1. Pick a language file (`python/python.go` or `latex/latex.go`). Define a struct that satisfies `core.Rule` (ID, Description, DefaultSeverity, Check).
2. Append the new struct to the language's `Analyzer.Rules()` slice — that's the only registration point.
3. Rule ID prefix: `py/` for Python, `tex/` for LaTeX. **IDs end up in users' config and inline directives, so treat them as a stable contract** (per `design.md` §16-5).
4. For Python: write a tree-sitter query and iterate captures via `tsutil.Query(ctx.Tree, ctx.File.Bytes, queryStr, lang)`. Use `nodeRange` / `tsutil.NodeRange` for ranges. Don't re-filter comments — the runner does it.
5. For LaTeX: use a `regexp.Regexp` and `findAll(re, ctx.File.Bytes, ctx.CommentRanges)`, then `reportAt(...)`. If the rule needs configuration, read it via `ctx.RuleConfig(r.ID()).Strings("key")` etc.

## Gotchas

- LaTeX comments cover the **entire line tail** including the `%`. Diagnostics emitted at the `%` itself or after it on the same line are silently dropped by the runner.
- `runner.extToLang` (in `runner/`) and `detect.extMap` (in `detect/`) duplicate the extension→language mapping. Keep them in sync if you add a language.
- `straightQuotes` flags every `"`, including ones in math mode or verbatim — there is currently no math/verbatim-aware filtering. If a user reports false positives, that's the cause.

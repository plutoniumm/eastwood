// Package svelte provides the Svelte language analyzer and all built-in rules.
// When built without the ts_svelte tag, rules use text/regex analysis.
// When built with -tags ts_svelte (and grammar C sources present), full
// tree-sitter parsing is available.
package svelte

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"eastwood/core"
	"eastwood/svelte/grammar"
	"eastwood/tsutil"

	sitter "github.com/smacker/go-tree-sitter"
)

// Analyzer implements core.Analyzer for Svelte component files.
type Analyzer struct{}

func (Analyzer) Language() string     { return "svelte" }
func (Analyzer) Extensions() []string { return []string{".svelte"} }

func (Analyzer) Parse(src []byte, _ string) (*sitter.Tree, error) {
	lang := grammar.GetLanguage()
	if lang == nil {
		return nil, nil // text-only mode
	}
	parser := sitter.NewParser()
	parser.SetLanguage(lang)
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, fmt.Errorf("svelte parse: %w", err)
	}
	return tree, nil
}

func (Analyzer) CommentRanges(src []byte, tree *sitter.Tree) []core.ByteRange {
	if tree != nil {
		var ranges []core.ByteRange
		ranges = append(ranges, tsutil.CommentRangesFromTree(tree, "comment")...)
		return ranges
	}
	return htmlCommentRanges(src)
}

func (Analyzer) Rules() []core.Rule {
	return []core.Rule{
		svelteMissingKey{},
		svelteNoAtHTML{},
		svelteNoAtDebug{},
		svelteImgAlt{},
		svelteClickKey{},
		svelteButtonType{},
		svelteNoReactiveReassign{},
		svelteDuplicateOn{},
		svelteStylePosition{},
		svelteScriptPosition{},
	}
}

// --- HTML comment scanner (text-mode fallback) ---

var htmlCommentRe = regexp.MustCompile(`<!--.*?-->`)

func htmlCommentRanges(src []byte) []core.ByteRange {
	var ranges []core.ByteRange
	// Also capture JS // and /* */ comments inside <script>.
	// For v1 this is a best-effort scan.
	for _, m := range htmlCommentRe.FindAllIndex(src, -1) {
		ranges = append(ranges, core.ByteRange{Start: m[0], End: m[1]})
	}
	return ranges
}

// --- shared text helpers ---

type lineInfo struct{ starts []int }

func buildLineInfo(src []byte) lineInfo {
	starts := []int{0}
	for i, b := range src {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return lineInfo{starts: starts}
}

func (li lineInfo) positionAt(offset int, file string) core.Position {
	line := sort.Search(len(li.starts), func(i int) bool { return li.starts[i] > offset }) - 1
	if line < 0 {
		line = 0
	}
	return core.Position{File: file, Line: line + 1, Col: offset - li.starts[line] + 1, Offset: offset}
}

func inComment(offset int, comments []core.ByteRange) bool {
	for _, r := range comments {
		if r.Contains(offset) {
			return true
		}
	}
	return false
}

func reportAt(ctx *core.RunContext, ruleID, msg string, sev core.Severity, start, end int) {
	li := buildLineInfo(ctx.File.Bytes)
	ctx.Report(core.Diagnostic{
		RuleID:   ruleID,
		Severity: sev,
		Message:  msg,
		Range: core.Range{
			Start: li.positionAt(start, ctx.File.Path),
			End:   li.positionAt(end, ctx.File.Path),
		},
	})
}

func findAll(re *regexp.Regexp, src []byte, comments []core.ByteRange) [][]int {
	var out [][]int
	for _, loc := range re.FindAllIndex(src, -1) {
		if !inComment(loc[0], comments) {
			out = append(out, loc)
		}
	}
	return out
}

// --- rule: svelte/missing-key ---

type svelteMissingKey struct{}

// Matches {#each expr as item} without a trailing (key)
var eachNoKeyRe = regexp.MustCompile(`\{#each\s+[^}]+\bas\b[^(}]+\}`)
var eachWithKeyRe = regexp.MustCompile(`\{#each\s+[^}]+\bas\b[^}]+\([^)]+\)\s*\}`)

func (svelteMissingKey) ID() string               { return "svelte/missing-key" }
func (svelteMissingKey) Description() string      { return "{#each} block without a key expression" }
func (svelteMissingKey) DefaultSeverity() core.Severity { return core.Warning }

func (r svelteMissingKey) Check(ctx *core.RunContext) {
	src := ctx.File.Bytes
	// Find all {#each} blocks that lack a (key).
	for _, loc := range findAll(eachNoKeyRe, src, ctx.CommentRanges) {
		block := string(src[loc[0]:loc[1]])
		if eachWithKeyRe.MatchString(block) {
			continue // has key
		}
		reportAt(ctx, r.ID(),
			"{#each} block missing a key expression; add (item.id) for efficient DOM diffing",
			r.DefaultSeverity(), loc[0], loc[1])
	}
}

// --- rule: svelte/no-at-html ---

type svelteNoAtHTML struct{}

var atHTMLRe = regexp.MustCompile(`\{@html\s`)

func (svelteNoAtHTML) ID() string               { return "svelte/no-at-html" }
func (svelteNoAtHTML) Description() string      { return "{@html} usage; XSS risk if content is user-supplied" }
func (svelteNoAtHTML) DefaultSeverity() core.Severity { return core.Warning }

func (r svelteNoAtHTML) Check(ctx *core.RunContext) {
	for _, loc := range findAll(atHTMLRe, ctx.File.Bytes, ctx.CommentRanges) {
		reportAt(ctx, r.ID(),
			"{@html} renders raw HTML; ensure content is sanitised to prevent XSS",
			r.DefaultSeverity(), loc[0], loc[1])
	}
}

// --- rule: svelte/no-at-debug ---

type svelteNoAtDebug struct{}

var atDebugRe = regexp.MustCompile(`\{@debug\b`)

func (svelteNoAtDebug) ID() string               { return "svelte/no-at-debug" }
func (svelteNoAtDebug) Description() string      { return "{@debug} tag left in component" }
func (svelteNoAtDebug) DefaultSeverity() core.Severity { return core.Warning }

func (r svelteNoAtDebug) Check(ctx *core.RunContext) {
	for _, loc := range findAll(atDebugRe, ctx.File.Bytes, ctx.CommentRanges) {
		reportAt(ctx, r.ID(), "{@debug} left in component; remove before shipping",
			r.DefaultSeverity(), loc[0], loc[1])
	}
}

// --- rule: svelte/a11y-img-alt ---

type svelteImgAlt struct{}

// Matches <img ...> tags that don't contain alt=
var imgTagRe = regexp.MustCompile(`(?i)<img\b[^>]*>`)
var imgAltRe = regexp.MustCompile(`(?i)\balt\s*=`)

func (svelteImgAlt) ID() string               { return "svelte/a11y-img-alt" }
func (svelteImgAlt) Description() string      { return "<img> element missing alt attribute" }
func (svelteImgAlt) DefaultSeverity() core.Severity { return core.Warning }

func (r svelteImgAlt) Check(ctx *core.RunContext) {
	src := ctx.File.Bytes
	for _, loc := range findAll(imgTagRe, src, ctx.CommentRanges) {
		tag := string(src[loc[0]:loc[1]])
		if !imgAltRe.MatchString(tag) {
			reportAt(ctx, r.ID(),
				"<img> is missing an alt attribute; required for screen reader accessibility",
				r.DefaultSeverity(), loc[0], loc[1])
		}
	}
}

// --- rule: svelte/a11y-click-key ---

type svelteClickKey struct{}

// Matches on:click handlers
var onClickRe = regexp.MustCompile(`\bon:click\b`)
var onKeyRe = regexp.MustCompile(`\bon:key(?:down|up|press)\b`)

func (svelteClickKey) ID() string               { return "svelte/a11y-click-key" }
func (svelteClickKey) Description() string      { return "on:click without a keyboard event handler" }
func (svelteClickKey) DefaultSeverity() core.Severity { return core.Warning }

func (r svelteClickKey) Check(ctx *core.RunContext) {
	src := ctx.File.Bytes
	// Find each on:click and check if the surrounding element also has a key handler.
	for _, loc := range findAll(onClickRe, src, ctx.CommentRanges) {
		// Scan the enclosing tag (backwards to < and forward to >).
		tagStart := loc[0]
		for tagStart > 0 && src[tagStart] != '<' {
			tagStart--
		}
		tagEnd := loc[1]
		for tagEnd < len(src) && src[tagEnd] != '>' {
			tagEnd++
		}
		tag := src[tagStart : tagEnd+1]
		if !onKeyRe.Match(tag) {
			reportAt(ctx, r.ID(),
				"on:click without on:keydown/keyup makes the element inaccessible via keyboard",
				r.DefaultSeverity(), loc[0], loc[1])
		}
	}
}

// --- rule: svelte/button-type ---

type svelteButtonType struct{}

var buttonRe = regexp.MustCompile(`(?i)<button\b[^>]*>`)
var buttonTypeRe = regexp.MustCompile(`(?i)\btype\s*=`)

func (svelteButtonType) ID() string               { return "svelte/button-type" }
func (svelteButtonType) Description() string      { return "<button> without explicit type attribute" }
func (svelteButtonType) DefaultSeverity() core.Severity { return core.Warning }

func (r svelteButtonType) Check(ctx *core.RunContext) {
	src := ctx.File.Bytes
	for _, loc := range findAll(buttonRe, src, ctx.CommentRanges) {
		tag := string(src[loc[0]:loc[1]])
		if !buttonTypeRe.MatchString(tag) {
			reportAt(ctx, r.ID(),
				"<button> without type defaults to 'submit'; add type=\"button\" to prevent unintended form submission",
				r.DefaultSeverity(), loc[0], loc[1])
		}
	}
}

// --- rule: svelte/no-reactive-reassign ---

type svelteNoReactiveReassign struct{}

// Matches $: x = ... x ... patterns (variable appears on both sides)
var reactiveStmtRe = regexp.MustCompile(`\$:\s*(\w+)\s*=([^;]+)`)

func (svelteNoReactiveReassign) ID() string               { return "svelte/no-reactive-reassign" }
func (svelteNoReactiveReassign) Description() string      { return "reactive statement reassigns its own dependency (infinite loop risk)" }
func (svelteNoReactiveReassign) DefaultSeverity() core.Severity { return core.Error }

func (r svelteNoReactiveReassign) Check(ctx *core.RunContext) {
	src := ctx.File.Bytes
	for _, m := range reactiveStmtRe.FindAllSubmatchIndex(src, -1) {
		if inComment(m[0], ctx.CommentRanges) {
			continue
		}
		varName := string(src[m[2]:m[3]])
		rhs := string(src[m[4]:m[5]])
		// Check if the variable name appears in the RHS (simple text check).
		words := regexp.MustCompile(`\b` + regexp.QuoteMeta(varName) + `\b`)
		if words.MatchString(rhs) {
			reportAt(ctx, r.ID(),
				fmt.Sprintf("$: %s = ... references %s on the right-hand side, creating an infinite reactive loop", varName, varName),
				r.DefaultSeverity(), m[0], m[1])
		}
	}
}

// --- rule: svelte/duplicate-on ---

type svelteDuplicateOn struct{}

var onHandlerRe = regexp.MustCompile(`\bon:(\w+)\b`)

func (svelteDuplicateOn) ID() string               { return "svelte/duplicate-on" }
func (svelteDuplicateOn) Description() string      { return "duplicate event handler on the same element" }
func (svelteDuplicateOn) DefaultSeverity() core.Severity { return core.Warning }

func (r svelteDuplicateOn) Check(ctx *core.RunContext) {
	src := ctx.File.Bytes
	li := buildLineInfo(src)
	// Scan tag by tag.
	tagRe := regexp.MustCompile(`<\w[^>]*>`)
	for _, tagLoc := range tagRe.FindAllIndex(src, -1) {
		if inComment(tagLoc[0], ctx.CommentRanges) {
			continue
		}
		tag := src[tagLoc[0]:tagLoc[1]]
		seen := map[string]bool{}
		for _, m := range onHandlerRe.FindAllSubmatch(tag, -1) {
			event := strings.ToLower(string(m[1]))
			if seen[event] {
				pos := li.positionAt(tagLoc[0], ctx.File.Path)
				ctx.Report(core.Diagnostic{
					RuleID:   r.ID(),
					Severity: r.DefaultSeverity(),
					Message:  fmt.Sprintf("on:%s appears more than once on this element; only the last handler runs", event),
					Range:    core.Range{Start: pos, End: li.positionAt(tagLoc[1], ctx.File.Path)},
				})
				break
			}
			seen[event] = true
		}
	}
}

// --- rule: svelte/style-position ---

type svelteStylePosition struct{}

var styleTagRe = regexp.MustCompile(`(?i)<style[\s>]`)

func (svelteStylePosition) ID() string               { return "svelte/style-position" }
func (svelteStylePosition) Description() string      { return "<style> block not at the top level of the component" }
func (svelteStylePosition) DefaultSeverity() core.Severity { return core.Info }

func (r svelteStylePosition) Check(ctx *core.RunContext) {
	src := ctx.File.Bytes
	locs := styleTagRe.FindAllIndex(src, -1)
	if len(locs) == 0 {
		return
	}
	li := buildLineInfo(src)
	for _, loc := range locs {
		if inComment(loc[0], ctx.CommentRanges) {
			continue
		}
		pos := li.positionAt(loc[0], ctx.File.Path)
		// Warn if there is non-whitespace content before the first <style>.
		before := strings.TrimSpace(string(src[:loc[0]]))
		if before != "" && !strings.HasPrefix(before, "<script") {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "<style> block should appear at the top level; Svelte convention is <script>, <style>, markup",
				Range:    core.Range{Start: pos, End: li.positionAt(loc[1], ctx.File.Path)},
			})
		}
	}
}

// --- rule: svelte/script-position ---

type svelteScriptPosition struct{}

var scriptTagRe = regexp.MustCompile(`(?i)<script[\s>]`)

func (svelteScriptPosition) ID() string               { return "svelte/script-position" }
func (svelteScriptPosition) Description() string      { return "<script> block not at the top of the component" }
func (svelteScriptPosition) DefaultSeverity() core.Severity { return core.Info }

func (r svelteScriptPosition) Check(ctx *core.RunContext) {
	src := ctx.File.Bytes
	locs := scriptTagRe.FindAllIndex(src, -1)
	if len(locs) == 0 {
		return
	}
	li := buildLineInfo(src)
	for _, loc := range locs {
		if inComment(loc[0], ctx.CommentRanges) {
			continue
		}
		before := strings.TrimSpace(string(src[:loc[0]]))
		if before != "" {
			pos := li.positionAt(loc[0], ctx.File.Path)
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "<script> block should be the first element in a Svelte component",
				Range:    core.Range{Start: pos, End: li.positionAt(loc[1], ctx.File.Path)},
			})
		}
	}
}

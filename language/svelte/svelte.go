// Package svelte provides the Svelte language analyzer and all built-in rules.
// When built without the ts_svelte tag, markup rules use text/regex analysis
// and <script> blocks are parsed with the TypeScript grammar (offsets
// preserved by masking). When built with -tags ts_svelte (and grammar C
// sources present), full tree-sitter-svelte parsing is available.
package svelte

import (
	"fmt"
	"regexp"
	"strings"

	"eastwood/core"
	"eastwood/language/javascript"
	"eastwood/language/svelte/grammar"
	"eastwood/tsutil"

	sitter "github.com/smacker/go-tree-sitter"
)

// Analyzer implements core.Analyzer for Svelte component files.
type Analyzer struct{}

func (Analyzer) Language() string     { return "svelte" }
func (Analyzer) Extensions() []string { return []string{".svelte"} }

var sveltePool = func() *tsutil.ParserPool {
	if l := grammar.GetLanguage(); l != nil {
		return tsutil.NewParserPool(l)
	}
	return nil
}()

func (Analyzer) Parse(src []byte, _ string) (*sitter.Tree, error) {
	if sveltePool != nil {
		tree, err := sveltePool.ParseBytes(src)
		if err != nil {
			return nil, fmt.Errorf("svelte parse: %w", err)
		}
		return tree, nil
	}
	// Text mode: parse <script> blocks with the TS grammar. Bytes outside the
	// blocks are blanked (newlines kept) so node offsets match the original
	// file, which lets the shared JS/TS rules run on component scripts.
	masked, ok := maskNonScript(src)
	if !ok {
		return nil, nil
	}
	return javascript.ParseEmbeddedScript(masked)
}

func (Analyzer) CommentRanges(src []byte, tree *sitter.Tree) []core.ByteRange {
	// HTML comments in the markup plus script comments from the tree.
	ranges := htmlCommentRanges(src)
	if tree != nil {
		ranges = append(ranges, tsutil.CommentRangesFromTree(tree, "comment")...)
	}
	return ranges
}

func (Analyzer) Rules() []core.Rule {
	rules := []core.Rule{
		missingKeyRule(),
		patternRule("svelte/no-at-html", "{@html} usage; XSS risk if content is user-supplied", core.Warning,
			atHTMLRe, "{@html} renders raw HTML; ensure content is sanitised to prevent XSS"),
		patternRule("svelte/no-at-debug", "{@debug} tag left in component", core.Warning,
			atDebugRe, "{@debug} left in component; remove before shipping"),
		tagMissingAttrRule("svelte/a11y-img-alt", "<img> element missing alt attribute", imgTagRe, imgAltRe,
			"<img> is missing an alt attribute; required for screen reader accessibility"),
		clickKeyRule(),
		tagMissingAttrRule("svelte/button-type", "<button> without explicit type attribute", buttonRe, buttonTypeRe,
			"<button> without type defaults to 'submit'; add type=\"button\" to prevent unintended form submission"),
		reactiveReassignRule(),
		duplicateOnRule(),
		positionRule("svelte/style-position", "<style> block not at the top level of the component", styleTagRe,
			"<style> block should appear at the top level; Svelte convention is <script>, <style>, markup",
			func(before string) bool { return before != "" && !strings.HasPrefix(before, "<script") }),
		positionRule("svelte/script-position", "<script> block not at the top of the component", scriptTagRe,
			"<script> block should be the first element in a Svelte component",
			func(before string) bool { return before != "" }),
		attrsPerLineRule(),
		tagBracketRule(),
	}
	for _, r := range javascript.EmbeddedScriptRules() {
		rules = append(rules, scriptRule{inner: r})
	}
	return rules
}

// --- HTML comment scanner (text-mode fallback) ---

var htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

func htmlCommentRanges(src []byte) []core.ByteRange {
	var ranges []core.ByteRange
	for _, m := range htmlCommentRe.FindAllIndex(src, -1) {
		ranges = append(ranges, core.ByteRange{Start: m[0], End: m[1]})
	}
	return ranges
}

// --- rule builders ---

// patternRule flags every match of re outside comments with a fixed message.
func patternRule(id, desc string, sev core.Severity, re *regexp.Regexp, msg string) core.Rule {
	return core.NewRule(id, desc, sev, func(r core.Rule, ctx *core.RunContext) {
		for _, loc := range core.FindAll(re, ctx.File.Bytes, ctx.CommentRanges) {
			ctx.ReportAt(r, msg, loc[0], loc[1])
		}
	})
}

// tagMissingAttrRule flags tags matching tagRe that lack an attribute
// matching attrRe.
func tagMissingAttrRule(id, desc string, tagRe, attrRe *regexp.Regexp, msg string) core.Rule {
	return core.NewRule(id, desc, core.Warning, func(r core.Rule, ctx *core.RunContext) {
		src := ctx.File.Bytes
		for _, loc := range core.FindAll(tagRe, src, ctx.CommentRanges) {
			if !attrRe.Match(src[loc[0]:loc[1]]) {
				ctx.ReportAt(r, msg, loc[0], loc[1])
			}
		}
	})
}

// positionRule flags blocks (style/script) preceded by unexpected content.
func positionRule(id, desc string, tagRe *regexp.Regexp, msg string, badBefore func(string) bool) core.Rule {
	return core.NewRule(id, desc, core.Info, func(r core.Rule, ctx *core.RunContext) {
		src := ctx.File.Bytes
		for _, loc := range tagRe.FindAllIndex(src, -1) {
			if core.InAnyRange(loc[0], ctx.CommentRanges) {
				continue
			}
			before := strings.TrimSpace(string(src[:loc[0]]))
			if badBefore(before) {
				ctx.ReportAt(r, msg, loc[0], loc[1])
			}
		}
	})
}

// --- rule: svelte/missing-key ---

// Matches {#each expr as item} without a trailing (key)
var eachNoKeyRe = regexp.MustCompile(`\{#each\s+[^}]+\bas\b[^(}]+\}`)
var eachWithKeyRe = regexp.MustCompile(`\{#each\s+[^}]+\bas\b[^}]+\([^)]+\)\s*\}`)

func missingKeyRule() core.Rule {
	return core.NewRule("svelte/missing-key", "{#each} block without a key expression", core.Warning,
		func(r core.Rule, ctx *core.RunContext) {
			src := ctx.File.Bytes
			for _, loc := range core.FindAll(eachNoKeyRe, src, ctx.CommentRanges) {
				if eachWithKeyRe.Match(src[loc[0]:loc[1]]) {
					continue // has key
				}
				ctx.ReportAt(r, "{#each} block missing a key expression; add (item.id) for efficient DOM diffing",
					loc[0], loc[1])
			}
		})
}

// --- rule: svelte/no-at-html / no-at-debug ---

var atHTMLRe = regexp.MustCompile(`\{@html\s`)
var atDebugRe = regexp.MustCompile(`\{@debug\b`)

// --- rule: svelte/a11y-img-alt / button-type ---

var imgTagRe = regexp.MustCompile(`(?i)<img\b[^>]*>`)
var imgAltRe = regexp.MustCompile(`(?i)\balt\s*=`)
var buttonRe = regexp.MustCompile(`(?i)<button\b[^>]*>`)
var buttonTypeRe = regexp.MustCompile(`(?i)\btype\s*=`)

// --- rule: svelte/a11y-click-key ---

var onClickRe = regexp.MustCompile(`\bon:click\b`)
var onKeyRe = regexp.MustCompile(`\bon:key(?:down|up|press)\b`)

func clickKeyRule() core.Rule {
	return core.NewRule("svelte/a11y-click-key", "on:click without a keyboard event handler", core.Warning,
		func(r core.Rule, ctx *core.RunContext) {
			src := ctx.File.Bytes
			// Find each on:click and check if the surrounding element also has a key handler.
			for _, loc := range core.FindAll(onClickRe, src, ctx.CommentRanges) {
				// Scan the enclosing tag (backwards to < and forward to >).
				tagStart := loc[0]
				for tagStart > 0 && src[tagStart] != '<' {
					tagStart--
				}
				tagEnd := loc[1]
				for tagEnd < len(src) && src[tagEnd] != '>' {
					tagEnd++
				}
				if !onKeyRe.Match(src[tagStart:min(tagEnd+1, len(src))]) {
					ctx.ReportAt(r, "on:click without on:keydown/keyup makes the element inaccessible via keyboard",
						loc[0], loc[1])
				}
			}
		})
}

// --- rule: svelte/no-reactive-reassign ---

// Matches $: x = ... patterns
var reactiveStmtRe = regexp.MustCompile(`\$:\s*(\w+)\s*=([^;]+)`)

func reactiveReassignRule() core.Rule {
	return core.NewRule("svelte/no-reactive-reassign",
		"reactive statement reassigns its own dependency (infinite loop risk)", core.Error,
		func(r core.Rule, ctx *core.RunContext) {
			src := ctx.File.Bytes
			for _, m := range reactiveStmtRe.FindAllSubmatchIndex(src, -1) {
				if core.InAnyRange(m[0], ctx.CommentRanges) {
					continue
				}
				varName := string(src[m[2]:m[3]])
				rhs := string(src[m[4]:m[5]])
				// Check if the variable name appears in the RHS (simple text check).
				words := regexp.MustCompile(`\b` + regexp.QuoteMeta(varName) + `\b`)
				if words.MatchString(rhs) {
					ctx.ReportAt(r,
						fmt.Sprintf("$: %s = ... references %s on the right-hand side, creating an infinite reactive loop", varName, varName),
						m[0], m[1])
				}
			}
		})
}

// --- rule: svelte/duplicate-on ---

var onHandlerRe = regexp.MustCompile(`\bon:(\w+)\b`)
var anyTagRe = regexp.MustCompile(`<\w[^>]*>`)

func duplicateOnRule() core.Rule {
	return core.NewRule("svelte/duplicate-on", "duplicate event handler on the same element", core.Warning,
		func(r core.Rule, ctx *core.RunContext) {
			src := ctx.File.Bytes
			for _, tagLoc := range anyTagRe.FindAllIndex(src, -1) {
				if core.InAnyRange(tagLoc[0], ctx.CommentRanges) {
					continue
				}
				tag := src[tagLoc[0]:tagLoc[1]]
				seen := map[string]bool{}
				for _, m := range onHandlerRe.FindAllSubmatch(tag, -1) {
					event := strings.ToLower(string(m[1]))
					if seen[event] {
						ctx.ReportAt(r,
							fmt.Sprintf("on:%s appears more than once on this element; only the last handler runs", event),
							tagLoc[0], tagLoc[1])
						break
					}
					seen[event] = true
				}
			}
		})
}

// --- rule: svelte/style-position / script-position ---

var styleTagRe = regexp.MustCompile(`(?i)<style[\s>]`)
var scriptTagRe = regexp.MustCompile(`(?i)<script[\s>]`)

// Package svelte provides the Svelte language analyzer.
//
// Native markup rules were intentionally removed: the Svelte formatter owns
// HTML layout (attribute placement, bracket style, block position) and would
// fight any linter enforcement. What remains is the shared JS/TS rule set run
// on component <script> blocks. Without the ts_svelte tag, <script> blocks are
// parsed with the TypeScript grammar (offsets preserved by masking); with
// -tags ts_svelte (and grammar C sources present), full tree-sitter-svelte
// parsing is available and the script bridge becomes a no-op.
package svelte

import (
	"fmt"
	"regexp"

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
	// Text mode: reduce the component to the JS it contains (script bodies +
	// template-tag expressions) and parse with the TS grammar. Offsets are
	// preserved, so the shared JS/TS rules run on component logic.
	masked, ok := maskToScript(src)
	if !ok {
		return nil, nil
	}
	return javascript.ParseEmbeddedScript(masked)
}

func (Analyzer) CommentRanges(src []byte, tree *sitter.Tree) []core.ByteRange {
	// HTML comments in the markup plus script comments from the tree, so
	// inline eastwood: directives work in either place.
	ranges := htmlCommentRanges(src)
	if tree != nil {
		ranges = append(ranges, tsutil.CommentRangesFromTree(tree, "comment")...)
	}
	return ranges
}

// Rules runs the shared JS/TS rule set on component <script> blocks. The
// scriptRule wrapper skips when the parse tree is a full Svelte document
// (ts_svelte builds), so these only fire in text mode.
func (Analyzer) Rules() []core.Rule {
	var rules []core.Rule
	for _, r := range javascript.EmbeddedScriptRules() {
		// boolean/ternary line-splitting can't be honoured in template
		// expressions (the Svelte formatter forces them inline), so that rule
		// applies to <script> bodies only.
		rules = append(rules, scriptRule{inner: r, scriptOnly: r.ID() == "js/boolean-layout"})
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

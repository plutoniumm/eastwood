// Markup layout rules (attribute placement, tag bracket style) and the
// <script>-block bridge that lets the shared JS/TS rules run on Svelte
// component scripts.
package svelte

import (
	"fmt"
	"regexp"

	"eastwood/core"
)

// --- <script> bridge ---

var scriptOpenRe = regexp.MustCompile(`(?i)<script\b[^>]*>`)
var scriptCloseRe = regexp.MustCompile(`(?i)</script\s*>`)

// maskNonScript returns a copy of src where every byte outside <script>
// blocks is blanked to a space (newlines kept), so the result parses as a
// plain script with node offsets identical to the original file.
func maskNonScript(src []byte) ([]byte, bool) {
	opens := scriptOpenRe.FindAllIndex(src, -1)
	if len(opens) == 0 {
		return nil, false
	}
	masked := make([]byte, len(src))
	for i, b := range src {
		if b == '\n' {
			masked[i] = '\n'
		} else {
			masked[i] = ' '
		}
	}
	found := false
	for _, o := range opens {
		rel := scriptCloseRe.FindIndex(src[o[1]:])
		if rel == nil {
			continue
		}
		copy(masked[o[1]:o[1]+rel[0]], src[o[1]:o[1]+rel[0]])
		found = true
	}
	return masked, found
}

// scriptRule wraps a shared JS/TS rule so it runs against the script tree
// produced by Analyzer.Parse in text mode. With the full tree-sitter-svelte
// grammar compiled in, the tree is a svelte document and these rules skip.
type scriptRule struct{ inner core.Rule }

func (r scriptRule) ID() string                     { return r.inner.ID() }
func (r scriptRule) Description() string            { return r.inner.Description() }
func (r scriptRule) DefaultSeverity() core.Severity { return r.inner.DefaultSeverity() }

func (r scriptRule) Check(ctx *core.RunContext) {
	if ctx.Tree == nil || ctx.Tree.RootNode().Type() != "program" {
		return
	}
	r.inner.Check(ctx)
}

// --- markup region helpers ---

var scriptBlockRe = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
var styleBlockRe = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`)

// markupExcluded returns the ranges markup rules must skip: script/style
// blocks plus comments.
func markupExcluded(src []byte, comments []core.ByteRange) []core.ByteRange {
	out := append([]core.ByteRange{}, comments...)
	for _, m := range scriptBlockRe.FindAllIndex(src, -1) {
		out = append(out, core.ByteRange{Start: m[0], End: m[1]})
	}
	for _, m := range styleBlockRe.FindAllIndex(src, -1) {
		out = append(out, core.ByteRange{Start: m[0], End: m[1]})
	}
	return out
}

// --- start tag scanner ---

type startTag struct {
	start, end int // byte range including < and >
	attrs      []core.ByteRange
}

func isNameStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isNameChar(c byte) bool {
	return isNameStart(c) || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ':' || c == '.'
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

const maxTagLen = 4096

// parseStartTag parses a start tag at src[i] ('<'). Attribute values are
// tracked through quotes and {expr} braces so on:click={() => x} counts as
// one attribute and its '>' does not end the tag.
func parseStartTag(src []byte, i int) (startTag, bool) {
	j := i + 1
	for j < len(src) && isNameChar(src[j]) {
		j++
	}
	tag := startTag{start: i}
	for {
		for j < len(src) && isSpaceByte(src[j]) {
			j++
		}
		if j >= len(src) || j-i > maxTagLen {
			return tag, false
		}
		switch {
		case src[j] == '>':
			tag.end = j + 1
			return tag, true
		case src[j] == '/' && j+1 < len(src) && src[j+1] == '>':
			tag.end = j + 2
			return tag, true
		case src[j] == '<':
			return tag, false // stray '<'; not a real tag
		}
		aStart := j
		var quote byte
		depth := 0
	attr:
		for j < len(src) && j-i <= maxTagLen {
			c := src[j]
			switch {
			case quote != 0:
				if c == quote {
					quote = 0
				}
			case c == '"' || c == '\'':
				quote = c
			case c == '{':
				depth++
			case c == '}':
				if depth > 0 {
					depth--
				}
			case depth == 0:
				if c == '>' || isSpaceByte(c) || (c == '/' && j+1 < len(src) && src[j+1] == '>') {
					break attr
				}
			}
			j++
		}
		if j >= len(src) || j-i > maxTagLen {
			return tag, false
		}
		tag.attrs = append(tag.attrs, core.ByteRange{Start: aStart, End: j})
	}
}

// scanStartTags finds all element start tags outside the excluded ranges.
func scanStartTags(src []byte, excluded []core.ByteRange) []startTag {
	var tags []startTag
	for i := 0; i < len(src)-1; i++ {
		if src[i] != '<' || !isNameStart(src[i+1]) || core.InAnyRange(i, excluded) {
			continue
		}
		if tag, ok := parseStartTag(src, i); ok {
			tags = append(tags, tag)
			i = tag.end - 1
		}
	}
	return tags
}

// --- rule: svelte/attrs-per-line ---

func attrsPerLineRule() core.Rule {
	return core.NewRule("svelte/attrs-per-line",
		"tag with more than 3 attributes should have one attribute per line", core.Warning,
		func(r core.Rule, ctx *core.RunContext) {
			src := ctx.File.Bytes
			excluded := markupExcluded(src, ctx.CommentRanges)
			for _, tag := range scanStartTags(src, excluded) {
				if len(tag.attrs) <= 3 {
					continue
				}
				// Every attribute on its own line, none sharing the tag-name line.
				seen := map[int]bool{ctx.PositionAt(tag.start).Line: true}
				bad := false
				for _, a := range tag.attrs {
					ln := ctx.PositionAt(a.Start).Line
					if seen[ln] {
						bad = true
						break
					}
					seen[ln] = true
				}
				if bad {
					ctx.ReportAt(r,
						fmt.Sprintf("tag has %d attributes; put each attribute on its own line", len(tag.attrs)),
						tag.start, tag.end)
				}
			}
		})
}

// --- rule: svelte/tag-bracket-style ---

var splitCloseRe = regexp.MustCompile(`</[A-Za-z][\w.:-]*$`)

func tagBracketRule() core.Rule {
	return core.NewRule("svelte/tag-bracket-style",
		"tag content hugging '>' or a split closing tag (prettier '\\n>abc<' style)", core.Warning,
		func(r core.Rule, ctx *core.RunContext) {
			src := ctx.File.Bytes
			excluded := markupExcluded(src, ctx.CommentRanges)
			start := 0
			for start <= len(src) {
				end := len(src)
				for k := start; k < len(src); k++ {
					if src[k] == '\n' {
						end = k
						break
					}
				}
				line := src[start:end]

				// Pattern 1: line opens with '>' followed by content (>abc...).
				t := 0
				for t < len(line) && isSpaceByte(line[t]) {
					t++
				}
				if t < len(line) && line[t] == '>' && !core.InAnyRange(start+t, excluded) {
					hasContent := false
					for _, c := range line[t+1:] {
						if !isSpaceByte(c) {
							hasContent = true
							break
						}
					}
					// Skip arrows (=>) split across lines.
					prevArrow := false
					for k := start - 2; k >= 0; k-- {
						if isSpaceByte(src[k]) {
							continue
						}
						prevArrow = src[k] == '='
						break
					}
					if hasContent && !prevArrow {
						ctx.ReportAt(r,
							"end the open tag with '>' on the previous line and put content on its own line",
							start+t, start+t+1)
					}
				}

				// Pattern 2: line ends with an unterminated closing tag (abc</span).
				e := len(line)
				for e > 0 && isSpaceByte(line[e-1]) {
					e--
				}
				if m := splitCloseRe.FindIndex(line[:e]); m != nil && !core.InAnyRange(start+m[0], excluded) {
					ctx.ReportAt(r,
						"closing tag is split across lines; keep the full closing tag together after the content line",
						start+m[0], start+e)
				}

				start = end + 1
			}
		})
}

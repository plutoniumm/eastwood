// The JS-extraction bridge. Svelte components are reduced to the JavaScript
// they contain — <script> bodies verbatim, plus the expression inside every
// template tag ({expr}, {#if cond}, {#each list as x}, ternaries, ...) — so the
// shared JS/TS rule set analyses component logic. Svelte control flow is JS, so
// it is treated as JS, not markup. Byte offsets are preserved (everything else
// is blanked to spaces, newlines kept), so diagnostic line/columns match the
// original file.
package svelte

import (
	"bytes"
	"regexp"

	"eastwood/core"
)

var scriptOpenRe = regexp.MustCompile(`(?i)<script\b[^>]*>`)
var scriptCloseRe = regexp.MustCompile(`(?i)</script\s*>`)
var styleBlockRe = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`)
var commentBlockRe = regexp.MustCompile(`(?s)<!--.*?-->`)

func isWS(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// maskToScript reduces src to the JavaScript it contains and returns it with
// byte offsets preserved. The bool is false only when there is nothing to
// analyse (no script and no template expressions).
func maskToScript(src []byte) ([]byte, bool) {
	masked := make([]byte, len(src))
	for i, b := range src {
		if b == '\n' {
			masked[i] = '\n'
		} else {
			masked[i] = ' '
		}
	}
	found := false

	// Ranges whose '{' must not be read as Svelte tags: <script> bodies (real
	// JS, revealed verbatim below), <style> blocks (CSS braces), and comments.
	var skip [][2]int
	reveal := func(s, e int) {
		copy(masked[s:e], src[s:e])
	}
	for _, o := range scriptOpenRe.FindAllIndex(src, -1) {
		rel := scriptCloseRe.FindIndex(src[o[1]:])
		if rel == nil {
			continue
		}
		bodyStart, bodyEnd := o[1], o[1]+rel[0]
		reveal(bodyStart, bodyEnd)
		skip = append(skip, [2]int{o[0], bodyEnd})
		found = true
	}
	for _, m := range styleBlockRe.FindAllIndex(src, -1) {
		skip = append(skip, [2]int{m[0], m[1]})
	}
	for _, m := range commentBlockRe.FindAllIndex(src, -1) {
		skip = append(skip, [2]int{m[0], m[1]})
	}
	skipped := func(i int) bool {
		for _, r := range skip {
			if i >= r[0] && i < r[1] {
				return true
			}
		}
		return false
	}

	// Reveal each Svelte template tag's expression as a ';'-terminated
	// statement so the JS parser sees a sequence of valid statements.
	for i := 0; i < len(src); i++ {
		if src[i] != '{' || skipped(i) {
			continue
		}
		close := matchBrace(src, i)
		if close < 0 {
			continue
		}
		if es, ee := svelteExprSpan(src, i+1, close); es < ee {
			reveal(es, ee)
			masked[close] = ';'
			found = true
		}
		i = close
	}
	return masked, found
}

// matchBrace returns the index of the '}' that closes the '{' at open, with
// brace-depth counting and string skipping ("", ”, “).
func matchBrace(src []byte, open int) int {
	depth := 0
	var q byte
	for i := open; i < len(src); i++ {
		c := src[i]
		if q != 0 {
			if c == q {
				q = 0
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			q = c
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// svelteExprSpan picks the JS-expression sub-span of a template tag's body
// src[start:end] (between the braces). Returns an empty span for tags that
// carry no analysable expression (closers, {:else}, {:then x}, bindings).
func svelteExprSpan(src []byte, start, end int) (int, int) {
	for start < end && isWS(src[start]) {
		start++
	}
	none := func() (int, int) { return start, start }
	if start >= end {
		return none()
	}
	seg := src[start:end]

	switch seg[0] {
	case '/': // {/if} {/each} {/await} {/key}
		return none()
	case '#':
		tok, rest := leadingToken(seg)
		body := start + rest
		switch tok {
		case "#if", "#key":
			return body, end
		case "#await":
			return body, kwBoundary(src, body, end, " then ", " catch ")
		case "#each":
			return body, kwBoundary(src, body, end, " as ")
		default: // #snippet and unknown blocks: no plain expression
			return none()
		}
	case ':':
		tok, rest := leadingToken(seg)
		if tok == ":else" {
			if t2, r2 := leadingToken(seg[rest:]); t2 == "if" {
				return start + rest + r2, end // {:else if cond}
			}
		}
		return none() // {:else} {:then x} {:catch e}
	case '@':
		tok, rest := leadingToken(seg)
		switch tok {
		case "@html", "@render", "@const", "@debug":
			return start + rest, end
		default:
			return none()
		}
	default: // plain mustache {expr}
		return start, end
	}
}

// leadingToken returns the first non-space token of seg and the offset where
// the following content begins (past the token and any spaces after it).
func leadingToken(seg []byte) (string, int) {
	i := 0
	for i < len(seg) && !isWS(seg[i]) {
		i++
	}
	tok := string(seg[:i])
	for i < len(seg) && isWS(seg[i]) {
		i++
	}
	return tok, i
}

// kwBoundary returns the index in [from,end) where the first of kws appears
// (e.g. " as " for #each), or end if none — the expression to reveal is
// src[from:result].
func kwBoundary(src []byte, from, end int, kws ...string) int {
	region := src[from:end]
	best := end
	for _, kw := range kws {
		if idx := bytes.Index(region, []byte(kw)); idx >= 0 && from+idx < best {
			best = from + idx
		}
	}
	return best
}

// scriptRule wraps a shared JS/TS rule so it runs against the JS extracted from
// a Svelte component in text mode. With the full tree-sitter-svelte grammar
// compiled in, the tree is a svelte document and these rules skip.
//
// scriptOnly rules apply only to <script> bodies: their diagnostics that land
// in a template expression are dropped. Layout rules that the Svelte formatter
// forces inline in markup (boolean/ternary line-splitting) use this, so the
// rule still polices component <script> code but not `{#if a && b && c}`.
type scriptRule struct {
	inner      core.Rule
	scriptOnly bool
}

func (r scriptRule) ID() string                     { return r.inner.ID() }
func (r scriptRule) Description() string            { return r.inner.Description() }
func (r scriptRule) DefaultSeverity() core.Severity { return r.inner.DefaultSeverity() }

func (r scriptRule) Check(ctx *core.RunContext) {
	if ctx.Tree == nil || ctx.Tree.RootNode().Type() != "program" {
		return
	}
	if !r.scriptOnly {
		r.inner.Check(ctx)
		return
	}
	bodies := scriptBodyRanges(ctx.File.Bytes)
	prev := ctx.Report
	ctx.Report = func(d core.Diagnostic) {
		if core.InAnyRange(d.Range.Start.Offset, bodies) {
			prev(d)
		}
	}
	r.inner.Check(ctx)
	ctx.Report = prev
}

// scriptBodyRanges returns the byte ranges of every <script> block body.
func scriptBodyRanges(src []byte) []core.ByteRange {
	var rs []core.ByteRange
	for _, o := range scriptOpenRe.FindAllIndex(src, -1) {
		rel := scriptCloseRe.FindIndex(src[o[1]:])
		if rel == nil {
			continue
		}
		rs = append(rs, core.ByteRange{Start: o[1], End: o[1] + rel[0]})
	}
	return rs
}

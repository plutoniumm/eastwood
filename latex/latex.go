// Package latex provides the LaTeX language analyzer and all built-in rules.
// v1 uses text/regex analysis; tree-sitter-latex integration is deferred.
package latex

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"eastwood/core"

	sitter "github.com/smacker/go-tree-sitter"
)

// Analyzer implements core.Analyzer for LaTeX source files.
type Analyzer struct{}

func (Analyzer) Language() string     { return "latex" }
func (Analyzer) Extensions() []string { return []string{".tex", ".cls", ".sty", ".bib"} }

// Parse returns nil for v1; tree-sitter-latex is not yet integrated.
func (Analyzer) Parse(src []byte) (*sitter.Tree, error) { return nil, nil }

// CommentRanges scans for LaTeX comments (% to EOL, respecting \%).
func (Analyzer) CommentRanges(src []byte, _ *sitter.Tree) []core.ByteRange {
	return latexCommentRanges(src)
}

func (Analyzer) Rules() []core.Rule {
	return []core.Rule{
		doubleDollarMath{},
		missingNbspCite{},
		missingNbspFig{},
		straightQuotes{},
		multipleBlankLines{},
		mismatchedEnvironment{},
		spaceBeforePunct{},
		emptySection{},
		wideHyphenInRange{},
		inconsistentMathDelim{},
	}
}

// --- comment range scanner ---

// latexCommentRanges returns byte ranges for every LaTeX comment in src.
// A comment begins at an unescaped % and runs to (but not including) the
// following newline (or end of file).
func latexCommentRanges(src []byte) []core.ByteRange {
	var ranges []core.ByteRange
	i := 0
	for i < len(src) {
		switch src[i] {
		case '\\':
			i += 2 // next byte is escaped; skip it
		case '%':
			start := i
			for i < len(src) && src[i] != '\n' {
				i++
			}
			ranges = append(ranges, core.ByteRange{Start: start, End: i})
		default:
			i++
		}
	}
	return ranges
}

// --- shared text-rule helpers ---

// lineInfo caches per-line metadata for a source file.
type lineInfo struct {
	starts []int // byte offset of the start of each line (0-indexed)
}

func buildLineInfo(src []byte) lineInfo {
	starts := []int{0}
	for i, b := range src {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return lineInfo{starts: starts}
}

// positionAt converts a byte offset to a core.Position.
func (li lineInfo) positionAt(offset int, file string) core.Position {
	line := sort.Search(len(li.starts), func(i int) bool {
		return li.starts[i] > offset
	}) - 1
	if line < 0 {
		line = 0
	}
	col := offset - li.starts[line] + 1
	return core.Position{File: file, Line: line + 1, Col: col, Offset: offset}
}

// inComment reports whether offset falls within any of the given comment ranges.
func inComment(offset int, comments []core.ByteRange) bool {
	for _, r := range comments {
		if r.Contains(offset) {
			return true
		}
	}
	return false
}

// textRule is a helper that provides shared state to text-based LaTeX rules.
type textRule struct{}

// reportAt emits a diagnostic at a specific byte offset.
func reportAt(ctx *core.RunContext, ruleID, msg string, sev core.Severity, offset, endOffset int) {
	li := buildLineInfo(ctx.File.Bytes)
	ctx.Report(core.Diagnostic{
		RuleID:   ruleID,
		Severity: sev,
		Message:  msg,
		Range: core.Range{
			Start: li.positionAt(offset, ctx.File.Path),
			End:   li.positionAt(endOffset, ctx.File.Path),
		},
	})
}

// findAll returns all non-overlapping match byte ranges for re in src,
// skipping matches that fall within any comment range.
func findAll(re *regexp.Regexp, src []byte, comments []core.ByteRange) [][]int {
	var out [][]int
	for _, loc := range re.FindAllIndex(src, -1) {
		if !inComment(loc[0], comments) {
			out = append(out, loc)
		}
	}
	return out
}

// --- rule: tex/double-dollar-display-math ---

type doubleDollarMath struct{}

var doubleDollarRe = regexp.MustCompile(`\$\$`)

func (doubleDollarMath) ID() string               { return "tex/double-dollar-display-math" }
func (doubleDollarMath) Description() string      { return "use \\[...\\] instead of $$...$$" }
func (doubleDollarMath) DefaultSeverity() core.Severity { return core.Warning }

func (r doubleDollarMath) Check(ctx *core.RunContext) {
	src := ctx.File.Bytes
	matches := doubleDollarRe.FindAllIndex(src, -1)
	// Emit on opening $$ only (every other occurrence).
	for i, loc := range matches {
		if i%2 != 0 {
			continue
		}
		if inComment(loc[0], ctx.CommentRanges) {
			continue
		}
		reportAt(ctx, r.ID(), "use \\[...\\] for display math instead of $$", r.DefaultSeverity(), loc[0], loc[1])
	}
}

// --- rule: tex/missing-nbsp-before-cite ---

type missingNbspCite struct{}

// Matches: a letter, then a space (not ~), then \cite
var nbspCiteRe = regexp.MustCompile(`[a-zA-Z] \\cite\b`)

func (missingNbspCite) ID() string               { return "tex/missing-nbsp-before-cite" }
func (missingNbspCite) Description() string      { return "missing non-breaking space before \\cite" }
func (missingNbspCite) DefaultSeverity() core.Severity { return core.Warning }

func (r missingNbspCite) Check(ctx *core.RunContext) {
	for _, loc := range findAll(nbspCiteRe, ctx.File.Bytes, ctx.CommentRanges) {
		// The space is at loc[0]+1
		reportAt(ctx, r.ID(), "use ~ instead of a space before \\cite", r.DefaultSeverity(), loc[0]+1, loc[0]+2)
	}
}

// --- rule: tex/missing-nbsp-after-fig ---

type missingNbspFig struct{}

// Matches: Fig., Eq., Sec., Tab., Alg., Lem., Thm., Def. followed by space/tab
var nbspFigRe = regexp.MustCompile(`\b(Fig|Eq|Sec|Tab|Alg|Lem|Thm|Def)\.[ \t]`)

func (missingNbspFig) ID() string               { return "tex/missing-nbsp-after-fig" }
func (missingNbspFig) Description() string      { return "missing non-breaking space after abbreviated cross-reference" }
func (missingNbspFig) DefaultSeverity() core.Severity { return core.Warning }

func (r missingNbspFig) Check(ctx *core.RunContext) {
	for _, loc := range findAll(nbspFigRe, ctx.File.Bytes, ctx.CommentRanges) {
		// The bad space is the last byte of the match.
		spaceOff := loc[1] - 1
		abbrev := string(ctx.File.Bytes[loc[0]:loc[1]-1])
		reportAt(ctx, r.ID(),
			fmt.Sprintf("use ~ instead of a space after %s (non-breaking space prevents line break)", abbrev),
			r.DefaultSeverity(), spaceOff, spaceOff+1)
	}
}

// --- rule: tex/straight-quotes ---

type straightQuotes struct{}

var straightQuoteRe = regexp.MustCompile(`"`)

func (straightQuotes) ID() string               { return "tex/straight-quotes" }
func (straightQuotes) Description() string      { return "straight double-quote instead of LaTeX quotes" }
func (straightQuotes) DefaultSeverity() core.Severity { return core.Warning }

func (r straightQuotes) Check(ctx *core.RunContext) {
	for _, loc := range findAll(straightQuoteRe, ctx.File.Bytes, ctx.CommentRanges) {
		reportAt(ctx, r.ID(), "use `` and '' for LaTeX quotation marks instead of \"", r.DefaultSeverity(), loc[0], loc[1])
	}
}

// --- rule: tex/multiple-blank-lines ---

type multipleBlankLines struct{}

func (multipleBlankLines) ID() string               { return "tex/multiple-blank-lines" }
func (multipleBlankLines) Description() string      { return "three or more consecutive blank lines" }
func (multipleBlankLines) DefaultSeverity() core.Severity { return core.Info }

func (r multipleBlankLines) Check(ctx *core.RunContext) {
	src := ctx.File.Bytes
	li := buildLineInfo(src)
	lines := strings.Split(string(src), "\n")
	consecutive := 0
	startLine := 0
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			if consecutive == 0 {
				startLine = i
			}
			consecutive++
		} else {
			if consecutive >= 3 {
				off := li.starts[startLine]
				reportAt(ctx, r.ID(),
					fmt.Sprintf("%d consecutive blank lines; consider reducing to one", consecutive),
					r.DefaultSeverity(), off, off+1)
			}
			consecutive = 0
		}
	}
}

// --- rule: tex/mismatched-environment ---

type mismatchedEnvironment struct{}

var beginEnvRe = regexp.MustCompile(`\\begin\{([^}]+)\}`)
var endEnvRe = regexp.MustCompile(`\\end\{([^}]+)\}`)

func (mismatchedEnvironment) ID() string               { return "tex/mismatched-environment" }
func (mismatchedEnvironment) Description() string      { return "\\begin without matching \\end" }
func (mismatchedEnvironment) DefaultSeverity() core.Severity { return core.Error }

func (r mismatchedEnvironment) Check(ctx *core.RunContext) {
	src := ctx.File.Bytes
	li := buildLineInfo(src)

	type envFrame struct {
		name   string
		offset int
	}
	var stack []envFrame

	// Merge begin and end events sorted by offset.
	type event struct {
		offset  int
		end     int
		name    string
		isBegin bool
	}
	var events []event
	for _, m := range beginEnvRe.FindAllSubmatchIndex(src, -1) {
		if inComment(m[0], ctx.CommentRanges) {
			continue
		}
		events = append(events, event{offset: m[0], end: m[1], name: string(src[m[2]:m[3]]), isBegin: true})
	}
	for _, m := range endEnvRe.FindAllSubmatchIndex(src, -1) {
		if inComment(m[0], ctx.CommentRanges) {
			continue
		}
		events = append(events, event{offset: m[0], end: m[1], name: string(src[m[2]:m[3]]), isBegin: false})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].offset < events[j].offset })

	for _, ev := range events {
		if ev.isBegin {
			stack = append(stack, envFrame{name: ev.name, offset: ev.offset})
		} else {
			if len(stack) == 0 {
				pos := li.positionAt(ev.offset, ctx.File.Path)
				ctx.Report(core.Diagnostic{
					RuleID:   r.ID(),
					Severity: r.DefaultSeverity(),
					Message:  fmt.Sprintf("\\end{%s} has no matching \\begin", ev.name),
					Range:    core.Range{Start: pos, End: li.positionAt(ev.end, ctx.File.Path)},
				})
				continue
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if top.name != ev.name {
				pos := li.positionAt(ev.offset, ctx.File.Path)
				ctx.Report(core.Diagnostic{
					RuleID:   r.ID(),
					Severity: r.DefaultSeverity(),
					Message:  fmt.Sprintf("\\end{%s} does not match \\begin{%s}", ev.name, top.name),
					Range:    core.Range{Start: pos, End: li.positionAt(ev.end, ctx.File.Path)},
				})
			}
		}
	}
	for _, frame := range stack {
		pos := li.positionAt(frame.offset, ctx.File.Path)
		ctx.Report(core.Diagnostic{
			RuleID:   r.ID(),
			Severity: r.DefaultSeverity(),
			Message:  fmt.Sprintf("\\begin{%s} has no matching \\end", frame.name),
			Range:    core.Range{Start: pos, End: li.positionAt(frame.offset+len(frame.name)+8, ctx.File.Path)},
		})
	}
}

// --- rule: tex/space-before-punctuation ---

type spaceBeforePunct struct{}

func (spaceBeforePunct) ID() string               { return "tex/space-before-punctuation" }
func (spaceBeforePunct) Description() string      { return "space immediately before punctuation" }
func (spaceBeforePunct) DefaultSeverity() core.Severity { return core.Warning }

func (r spaceBeforePunct) Check(ctx *core.RunContext) {
	cfg := ctx.RuleConfig(r.ID())
	allowed := cfg.Strings("allow_before")
	allowSet := make(map[byte]bool)
	for _, s := range allowed {
		if len(s) == 1 {
			allowSet[s[0]] = true
		}
	}

	src := ctx.File.Bytes
	for i := 1; i < len(src); i++ {
		if src[i-1] != ' ' && src[i-1] != '\t' {
			continue
		}
		punct := src[i]
		if punct != ',' && punct != '.' && punct != ';' && punct != '!' && punct != '?' && punct != ':' {
			continue
		}
		if allowSet[punct] {
			continue
		}
		if inComment(i-1, ctx.CommentRanges) {
			continue
		}
		reportAt(ctx, r.ID(),
			fmt.Sprintf("space before '%c'; remove or use ~ for intentional spacing", punct),
			r.DefaultSeverity(), i-1, i+1)
	}
}

// --- rule: tex/empty-section ---

type emptySection struct{}

var sectionCmdRe = regexp.MustCompile(`\\(chapter|section|subsection|subsubsection|paragraph)\*?\{[^}]*\}`)

func (emptySection) ID() string               { return "tex/empty-section" }
func (emptySection) Description() string      { return "section with no content before the next section" }
func (emptySection) DefaultSeverity() core.Severity { return core.Warning }

func (r emptySection) Check(ctx *core.RunContext) {
	src := ctx.File.Bytes
	li := buildLineInfo(src)
	matches := sectionCmdRe.FindAllIndex(src, -1)

	for i := 0; i+1 < len(matches); i++ {
		cur := matches[i]
		next := matches[i+1]
		if inComment(cur[0], ctx.CommentRanges) {
			continue
		}
		between := strings.TrimSpace(string(src[cur[1]:next[0]]))
		if between == "" {
			pos := li.positionAt(cur[0], ctx.File.Path)
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "section has no content before the next section heading",
				Range:    core.Range{Start: pos, End: li.positionAt(cur[1], ctx.File.Path)},
			})
		}
	}
}

// --- rule: tex/wide-hyphen-in-range (opt-in) ---

type wideHyphenInRange struct{}

// Matches digits separated by a single hyphen (not already en-dash --)
var hyphenRangeRe = regexp.MustCompile(`(\d+)-(\d+)`)
var enDashRe = regexp.MustCompile(`(\d+)--(\d+)`)

func (wideHyphenInRange) ID() string               { return "tex/wide-hyphen-in-range" }
func (wideHyphenInRange) Description() string      { return "single hyphen in numeric range; use en-dash (--)" }
func (wideHyphenInRange) DefaultSeverity() core.Severity { return core.Info }

func (r wideHyphenInRange) Check(ctx *core.RunContext) {
	src := ctx.File.Bytes
	// Build set of en-dash range positions to exclude.
	enDashSet := make(map[int]bool)
	for _, m := range enDashRe.FindAllIndex(src, -1) {
		enDashSet[m[0]] = true
	}
	for _, loc := range findAll(hyphenRangeRe, src, ctx.CommentRanges) {
		if enDashSet[loc[0]] {
			continue
		}
		reportAt(ctx, r.ID(),
			fmt.Sprintf("use -- (en-dash) for numeric range %s", string(src[loc[0]:loc[1]])),
			r.DefaultSeverity(), loc[0], loc[1])
	}
}

// --- rule: tex/inconsistent-math-delim ---

type inconsistentMathDelim struct{}

var dollarInlineRe = regexp.MustCompile(`\$[^$]+?\$`)
var parenInlineRe = regexp.MustCompile(`\\\(.*?\\\)`)

func (inconsistentMathDelim) ID() string               { return "tex/inconsistent-math-delim" }
func (inconsistentMathDelim) Description() string      { return "mixed inline math delimiters ($...$ and \\(...\\))" }
func (inconsistentMathDelim) DefaultSeverity() core.Severity { return core.Warning }

func (r inconsistentMathDelim) Check(ctx *core.RunContext) {
	src := ctx.File.Bytes
	dollarMatches := findAll(dollarInlineRe, src, ctx.CommentRanges)
	parenMatches := findAll(parenInlineRe, src, ctx.CommentRanges)

	hasDollar := len(dollarMatches) > 0
	hasParen := len(parenMatches) > 0

	if !hasDollar || !hasParen {
		return
	}

	// Report at the first occurrence of the minority style.
	var minorityOff int
	var majority, minority string
	if len(dollarMatches) >= len(parenMatches) {
		minority = `\(...\)`
		majority = `$...$`
		minorityOff = parenMatches[0][0]
	} else {
		minority = `$...$`
		majority = `\(...\)`
		minorityOff = dollarMatches[0][0]
	}

	reportAt(ctx, r.ID(),
		fmt.Sprintf("file uses both %s and %s for inline math; pick one (majority is %s)", minority, majority, majority),
		r.DefaultSeverity(), minorityOff, minorityOff+1)
}

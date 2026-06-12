package core

import (
	"fmt"
	"regexp"
	"sort"

	sitter "github.com/smacker/go-tree-sitter"
)

type Severity int

const (
	Info    Severity = iota
	Warning Severity = iota
	Error   Severity = iota
)

func (s Severity) String() string {
	switch s {
	case Info:
		return "info"
	case Warning:
		return "warning"
	case Error:
		return "error"
	default:
		return "unknown"
	}
}

func ParseSeverity(s string) (Severity, error) {
	switch s {
	case "info":
		return Info, nil
	case "warning":
		return Warning, nil
	case "error":
		return Error, nil
	default:
		return Warning, fmt.Errorf("unknown severity %q; want info|warning|error", s)
	}
}

type Position struct {
	File   string
	Line   int // 1-indexed
	Col    int // 1-indexed, rune column
	Offset int // byte offset from file start
}

type Range struct {
	Start, End Position
}

// ByteRange is a half-open [Start, End) byte interval within a file.
type ByteRange struct {
	Start, End int
}

func (r ByteRange) Contains(offset int) bool {
	return offset >= r.Start && offset < r.End
}

// InAnyRange reports whether offset falls inside any of the byte ranges.
func InAnyRange(offset int, ranges []ByteRange) bool {
	for _, r := range ranges {
		if r.Contains(offset) {
			return true
		}
	}
	return false
}

// FindAll returns the regex matches in src whose start does not fall inside
// any of the excluded ranges (typically comments).
func FindAll(re *regexp.Regexp, src []byte, excluded []ByteRange) [][]int {
	var out [][]int
	for _, loc := range re.FindAllIndex(src, -1) {
		if !InAnyRange(loc[0], excluded) {
			out = append(out, loc)
		}
	}
	return out
}

type Diagnostic struct {
	RuleID   string
	Severity Severity
	Message  string
	Range    Range
}

type SourceFile struct {
	Path     string
	Language string
	Bytes    []byte
}

// RuleConfig holds arbitrary per-rule TOML values decoded from eastwood.toml.
type RuleConfig map[string]any

func (rc RuleConfig) String(key, def string) string {
	if v, ok := rc[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func (rc RuleConfig) Strings(key string) []string {
	if v, ok := rc[key]; ok {
		switch t := v.(type) {
		case []string:
			return t
		case []any:
			out := make([]string, 0, len(t))
			for _, item := range t {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return nil
}

func (rc RuleConfig) Bool(key string, def bool) bool {
	if v, ok := rc[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// RunContext is passed to every rule's Check method for a single file.
type RunContext struct {
	File          *SourceFile
	Tree          *sitter.Tree // nil for analyzers without tree-sitter
	RuleConfigs   map[string]RuleConfig
	CommentRanges []ByteRange // byte ranges of comment nodes; text-scanning rules filter against these
	Report        func(Diagnostic)

	lineStarts []int // lazily built by PositionAt; rules run sequentially per file
}

// RuleConfig returns the config for the given rule ID (never nil).
func (ctx *RunContext) RuleConfig(id string) RuleConfig {
	if rc, ok := ctx.RuleConfigs[id]; ok {
		return rc
	}
	return RuleConfig{}
}

// PositionAt converts a byte offset into a 1-indexed line/column Position.
// The underlying line index is built once per file and shared by every rule.
func (ctx *RunContext) PositionAt(offset int) Position {
	if ctx.lineStarts == nil {
		starts := []int{0}
		for i, b := range ctx.File.Bytes {
			if b == '\n' {
				starts = append(starts, i+1)
			}
		}
		ctx.lineStarts = starts
	}
	line := sort.Search(len(ctx.lineStarts), func(i int) bool { return ctx.lineStarts[i] > offset }) - 1
	line = max(line, 0)
	return Position{
		File:   ctx.File.Path,
		Line:   line + 1,
		Col:    offset - ctx.lineStarts[line] + 1,
		Offset: offset,
	}
}

// ReportAt emits a diagnostic for rule r over the half-open byte range
// [start, end) at the rule's default severity.
func (ctx *RunContext) ReportAt(r Rule, msg string, start, end int) {
	ctx.ReportSev(r, r.DefaultSeverity(), msg, start, end)
}

// ReportSev is ReportAt with an explicit severity.
func (ctx *RunContext) ReportSev(r Rule, sev Severity, msg string, start, end int) {
	ctx.Report(Diagnostic{
		RuleID:   r.ID(),
		Severity: sev,
		Message:  msg,
		Range: Range{
			Start: ctx.PositionAt(start),
			End:   ctx.PositionAt(end),
		},
	})
}

type Rule interface {
	ID() string
	Description() string
	DefaultSeverity() Severity
	Check(ctx *RunContext)
}

// rule is the value type behind NewRule.
type rule struct {
	id, desc string
	sev      Severity
	check    func(Rule, *RunContext)
}

func (r rule) ID() string                { return r.id }
func (r rule) Description() string       { return r.desc }
func (r rule) DefaultSeverity() Severity { return r.sev }
func (r rule) Check(ctx *RunContext)     { r.check(r, ctx) }

// NewRule builds a Rule from its metadata and a check function. The check
// receives the rule itself so report helpers can stamp ID and severity,
// making a rule definition a single expression instead of a struct with
// four methods.
func NewRule(id, desc string, sev Severity, check func(Rule, *RunContext)) Rule {
	return rule{id: id, desc: desc, sev: sev, check: check}
}

type Analyzer interface {
	Language() string
	Extensions() []string
	// Parse parses src. path is the file path (may be "" for stdin) and lets
	// analyzers that support multiple grammars (e.g. ts vs tsx) pick the right one.
	Parse(src []byte, path string) (*sitter.Tree, error)
	CommentRanges(src []byte, tree *sitter.Tree) []ByteRange
	Rules() []Rule
}

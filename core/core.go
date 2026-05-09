package core

import (
	"fmt"

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
	Tree          *sitter.Tree   // nil for analyzers without tree-sitter
	RuleConfigs   map[string]RuleConfig
	CommentRanges []ByteRange    // byte ranges of comment nodes; diagnostics inside are suppressed
	Report        func(Diagnostic)
}

// RuleConfig returns the config for the given rule ID (never nil).
func (ctx *RunContext) RuleConfig(id string) RuleConfig {
	if rc, ok := ctx.RuleConfigs[id]; ok {
		return rc
	}
	return RuleConfig{}
}

type Rule interface {
	ID() string
	Description() string
	DefaultSeverity() Severity
	Check(ctx *RunContext)
}

type Analyzer interface {
	Language() string
	Extensions() []string
	Parse(src []byte) (*sitter.Tree, error)
	CommentRanges(src []byte, tree *sitter.Tree) []ByteRange
	Rules() []Rule
}

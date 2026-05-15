// Package python provides the Python language analyzer and all built-in rules.
package python

import (
	"context"
	"fmt"
	"strings"

	"eastwood/core"
	"eastwood/tsutil"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"
)

// Analyzer implements core.Analyzer for Python source files.
type Analyzer struct{}

func (Analyzer) Language() string          { return "python" }
func (Analyzer) Extensions() []string      { return []string{".py", ".pyi"} }

func (Analyzer) Parse(src []byte, _ string) (*sitter.Tree, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(python.GetLanguage())
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, fmt.Errorf("python parse: %w", err)
	}
	return tree, nil
}

func (Analyzer) CommentRanges(src []byte, tree *sitter.Tree) []core.ByteRange {
	if tree == nil {
		return nil
	}
	return tsutil.CommentRangesFromTree(tree, "comment")
}

func (Analyzer) Rules() []core.Rule {
	return []core.Rule{
		mutableDefaultArg{},
		bareExcept{},
		comparisonToNone{},
		comparisonToBool{},
		fstringNoPlaceholder{},
		printStatement{},
		emptyDocstring{},
		percentFormat{},
		redundantParensReturn{},
		assertTuple{},
	}
}

var lang = python.GetLanguage()

// --- helper ---

func nodeText(node *sitter.Node, src []byte) string {
	return string(src[node.StartByte():node.EndByte()])
}

func nodeRange(node *sitter.Node, src []byte, file string) core.Range {
	return tsutil.NodeRange(node, src, file)
}

// stripStringDelimiters removes surrounding quotes (single, double, triple)
// and optional f/r/b prefix from a Python string literal. Returns the inner
// content only.
func stripStringDelimiters(s string) string {
	lower := strings.ToLower(s)
	// Strip prefix characters: f, r, b, u (any combination)
	i := 0
	for i < len(lower) && strings.ContainsRune("frbu", rune(lower[i])) {
		i++
	}
	s = s[i:]
	for _, delim := range []string{`"""`, `'''`, `"`, `'`} {
		if strings.HasPrefix(s, delim) && strings.HasSuffix(s, delim) && len(s) >= len(delim)*2 {
			return s[len(delim) : len(s)-len(delim)]
		}
	}
	return s
}

// --- rule: py/mutable-default-arg ---

type mutableDefaultArg struct{}

const mutableDefaultQuery = `
(default_parameter
  value: [(list) (dictionary) (set)] @bad)
`

func (mutableDefaultArg) ID() string               { return "py/mutable-default-arg" }
func (mutableDefaultArg) Description() string      { return "mutable default argument" }
func (mutableDefaultArg) DefaultSeverity() core.Severity { return core.Warning }

func (r mutableDefaultArg) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, mutableDefaultQuery, lang) {
		ctx.Report(core.Diagnostic{
			RuleID:   r.ID(),
			Severity: r.DefaultSeverity(),
			Message:  fmt.Sprintf("mutable default argument (%s); use None and assign inside the function body", cap.Node.Type()),
			Range:    nodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
		})
	}
}

// --- rule: py/bare-except ---

type bareExcept struct{}

const bareExceptQuery = `(except_clause) @clause`

func (bareExcept) ID() string               { return "py/bare-except" }
func (bareExcept) Description() string      { return "bare except clause" }
func (bareExcept) DefaultSeverity() core.Severity { return core.Warning }

func (r bareExcept) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, bareExceptQuery, lang) {
		if cap.Node.ChildByFieldName("type") == nil {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "bare except clause catches all exceptions; specify an exception type",
				Range:    nodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: py/comparison-to-none ---

type comparisonToNone struct{}

const compNoneQuery = `(comparison_operator (none) @none) @cmp`

func (comparisonToNone) ID() string               { return "py/comparison-to-none" }
func (comparisonToNone) Description() string      { return "equality comparison to None" }
func (comparisonToNone) DefaultSeverity() core.Severity { return core.Warning }

func (r comparisonToNone) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, compNoneQuery, lang) {
		if cap.Name != "cmp" {
			continue
		}
		if op := findCompOp(cap.Node); op == "==" || op == "!=" {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  fmt.Sprintf("use 'is None' / 'is not None' instead of '%s None'", op),
				Range:    nodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// findCompOp returns the first comparison operator token found among a
// comparison_operator node's children.
func findCompOp(node *sitter.Node) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "==", "!=", "<", ">", "<=", ">=", "in", "not in", "is", "is not":
			return child.Type()
		}
	}
	return ""
}

// --- rule: py/comparison-to-bool ---

type comparisonToBool struct{}

const compBoolQuery = `(comparison_operator [(true) (false)] @bool) @cmp`

func (comparisonToBool) ID() string               { return "py/comparison-to-bool" }
func (comparisonToBool) Description() string      { return "equality comparison to boolean literal" }
func (comparisonToBool) DefaultSeverity() core.Severity { return core.Warning }

func (r comparisonToBool) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, compBoolQuery, lang) {
		if cap.Name != "cmp" {
			continue
		}
		if op := findCompOp(cap.Node); op == "==" || op == "!=" {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  fmt.Sprintf("use truthiness test instead of '%s True/False'", op),
				Range:    nodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: py/f-string-no-placeholder ---

type fstringNoPlaceholder struct{}

const fstringQuery = `(string) @str`

func (fstringNoPlaceholder) ID() string               { return "py/f-string-no-placeholder" }
func (fstringNoPlaceholder) Description() string      { return "f-string without any placeholders" }
func (fstringNoPlaceholder) DefaultSeverity() core.Severity { return core.Warning }

func (r fstringNoPlaceholder) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, fstringQuery, lang) {
		node := cap.Node
		raw := nodeText(node, ctx.File.Bytes)
		lowered := strings.ToLower(raw)
		if !strings.HasPrefix(lowered, "f") {
			continue
		}
		if hasChildType(node, "interpolation") {
			continue
		}
		ctx.Report(core.Diagnostic{
			RuleID:   r.ID(),
			Severity: r.DefaultSeverity(),
			Message:  "f-string has no placeholders; drop the 'f' prefix",
			Range:    nodeRange(node, ctx.File.Bytes, ctx.File.Path),
		})
	}
}

func hasChildType(node *sitter.Node, typeName string) bool {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		if node.NamedChild(i).Type() == typeName {
			return true
		}
	}
	return false
}

// --- rule: py/print-statement (disabled by default) ---

type printStatement struct{}

const printQuery = `(call function: (identifier) @fn)`

func (printStatement) ID() string               { return "py/print-statement" }
func (printStatement) Description() string      { return "bare print() call" }
func (printStatement) DefaultSeverity() core.Severity { return core.Info }

func (r printStatement) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, printQuery, lang) {
		if nodeText(cap.Node, ctx.File.Bytes) != "print" {
			continue
		}
		ctx.Report(core.Diagnostic{
			RuleID:   r.ID(),
			Severity: r.DefaultSeverity(),
			Message:  "print() call found; consider using a logger",
			Range:    nodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
		})
	}
}

// --- rule: py/empty-docstring ---

type emptyDocstring struct{}

const emptyDocQuery = `[(function_definition) (class_definition)] @defn`

func (emptyDocstring) ID() string               { return "py/empty-docstring" }
func (emptyDocstring) Description() string      { return "empty or whitespace-only docstring" }
func (emptyDocstring) DefaultSeverity() core.Severity { return core.Warning }

func (r emptyDocstring) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, emptyDocQuery, lang) {
		node := cap.Node
		body := node.ChildByFieldName("body")
		if body == nil || body.NamedChildCount() == 0 {
			continue
		}
		first := body.NamedChild(0)
		if first == nil || first.Type() != "expression_statement" {
			continue
		}
		expr := first.NamedChild(0)
		if expr == nil || expr.Type() != "string" {
			continue
		}
		inner := stripStringDelimiters(nodeText(expr, ctx.File.Bytes))
		if strings.TrimSpace(inner) == "" {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "empty or whitespace-only docstring",
				Range:    nodeRange(expr, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: py/percent-format ---

type percentFormat struct{}

const percentFormatQuery = `(binary_operator left: (string) operator: "%" ) @op`

func (percentFormat) ID() string               { return "py/percent-format" }
func (percentFormat) Description() string      { return "%-style string formatting" }
func (percentFormat) DefaultSeverity() core.Severity { return core.Warning }

func (r percentFormat) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, percentFormatQuery, lang) {
		ctx.Report(core.Diagnostic{
			RuleID:   r.ID(),
			Severity: r.DefaultSeverity(),
			Message:  "%-style formatting; prefer f-strings or str.format()",
			Range:    nodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
		})
	}
}

// --- rule: py/redundant-parens-return ---

type redundantParensReturn struct{}

const redundantParensQuery = `(return_statement (parenthesized_expression) @parens)`

func (redundantParensReturn) ID() string               { return "py/redundant-parens-return" }
func (redundantParensReturn) Description() string      { return "redundant parentheses in return statement" }
func (redundantParensReturn) DefaultSeverity() core.Severity { return core.Info }

func (r redundantParensReturn) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, redundantParensQuery, lang) {
		ctx.Report(core.Diagnostic{
			RuleID:   r.ID(),
			Severity: r.DefaultSeverity(),
			Message:  "redundant parentheses in return statement",
			Range:    nodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
		})
	}
}

// --- rule: py/assert-tuple ---

type assertTuple struct{}

const assertTupleQuery = `(assert_statement (tuple) @tup)`

func (assertTuple) ID() string               { return "py/assert-tuple" }
func (assertTuple) Description() string      { return "assert with a tuple is always True" }
func (assertTuple) DefaultSeverity() core.Severity { return core.Error }

func (r assertTuple) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, assertTupleQuery, lang) {
		ctx.Report(core.Diagnostic{
			RuleID:   r.ID(),
			Severity: r.DefaultSeverity(),
			Message:  "assert with a tuple is always True; did you mean 'assert cond, msg'?",
			Range:    nodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
		})
	}
}


// Package python provides the Python language analyzer and all built-in rules.
package python

import (
	"fmt"
	"strings"

	"eastwood/core"
	"eastwood/tsutil"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"
)

// Analyzer implements core.Analyzer for Python source files.
type Analyzer struct{}

func (Analyzer) Language() string     { return "python" }
func (Analyzer) Extensions() []string { return []string{".py", ".pyi"} }

func (Analyzer) Parse(src []byte, _ string) (*sitter.Tree, error) {
	tree, err := pool.ParseBytes(src)
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

var (
	lang = python.GetLanguage()
	pool = tsutil.NewParserPool(lang)

	mutableDefaultQ = tsutil.MustQuery(`
(default_parameter
  value: [(list) (dictionary) (set)] @bad)
`, lang)
	bareExceptQ      = tsutil.MustQuery(`(except_clause) @clause`, lang)
	compNoneQ        = tsutil.MustQuery(`(comparison_operator (none) @none) @cmp`, lang)
	compBoolQ        = tsutil.MustQuery(`(comparison_operator [(true) (false)] @bool) @cmp`, lang)
	stringQ          = tsutil.MustQuery(`(string) @str`, lang)
	identCallQ       = tsutil.MustQuery(`(call function: (identifier) @fn)`, lang)
	defnQ            = tsutil.MustQuery(`[(function_definition) (class_definition)] @defn`, lang)
	percentFormatQ   = tsutil.MustQuery(`(binary_operator left: (string) operator: "%" ) @op`, lang)
	redundantParensQ = tsutil.MustQuery(`(return_statement (parenthesized_expression) @parens)`, lang)
	assertTupleQ     = tsutil.MustQuery(`(assert_statement (tuple) @tup)`, lang)
)

// --- helpers ---

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

func hasChildType(node *sitter.Node, typeName string) bool {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		if node.NamedChild(i).Type() == typeName {
			return true
		}
	}
	return false
}

// equalityComparisonRule flags == / != comparisons against literals matched
// by q's @cmp capture (used by comparison-to-none and comparison-to-bool).
func equalityComparisonRule(id, desc string, q tsutil.CompiledQuery, msgf func(op string) string) core.Rule {
	return core.NewRule(id, desc, core.Warning, func(r core.Rule, ctx *core.RunContext) {
		for cap := range q.Run(ctx.Tree, ctx.File.Bytes) {
			if cap.Name != "cmp" {
				continue
			}
			if op := findCompOp(cap.Node); op == "==" || op == "!=" {
				tsutil.ReportNode(ctx, r, cap.Node, msgf(op))
			}
		}
	})
}

func (Analyzer) Rules() []core.Rule {
	return []core.Rule{
		tsutil.TrailingCommaRule("py/trailing-comma",
			"multiline dict literal without trailing comma", lang, "(dictionary) @dict"),

		core.NewRule("py/mutable-default-arg", "mutable default argument", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range mutableDefaultQ.Run(ctx.Tree, ctx.File.Bytes) {
					tsutil.ReportNode(ctx, r, cap.Node,
						fmt.Sprintf("mutable default argument (%s); use None and assign inside the function body", cap.Node.Type()))
				}
			}),

		core.NewRule("py/bare-except", "bare except clause", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range bareExceptQ.Run(ctx.Tree, ctx.File.Bytes) {
					if cap.Node.ChildByFieldName("type") == nil {
						tsutil.ReportNode(ctx, r, cap.Node, "bare except clause catches all exceptions; specify an exception type")
					}
				}
			}),

		equalityComparisonRule("py/comparison-to-none", "equality comparison to None", compNoneQ,
			func(op string) string {
				return fmt.Sprintf("use 'is None' / 'is not None' instead of '%s None'", op)
			}),

		equalityComparisonRule("py/comparison-to-bool", "equality comparison to boolean literal", compBoolQ,
			func(op string) string {
				return fmt.Sprintf("use truthiness test instead of '%s True/False'", op)
			}),

		core.NewRule("py/f-string-no-placeholder", "f-string without any placeholders", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range stringQ.Run(ctx.Tree, ctx.File.Bytes) {
					raw := tsutil.NodeText(cap.Node, ctx.File.Bytes)
					if !strings.HasPrefix(strings.ToLower(raw), "f") || hasChildType(cap.Node, "interpolation") {
						continue
					}
					tsutil.ReportNode(ctx, r, cap.Node, "f-string has no placeholders; drop the 'f' prefix")
				}
			}),

		core.NewRule("py/print-statement", "bare print() call", core.Info,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range identCallQ.Run(ctx.Tree, ctx.File.Bytes) {
					if tsutil.NodeText(cap.Node, ctx.File.Bytes) == "print" {
						tsutil.ReportNode(ctx, r, cap.Node, "print() call found; consider using a logger")
					}
				}
			}),

		core.NewRule("py/empty-docstring", "empty or whitespace-only docstring", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range defnQ.Run(ctx.Tree, ctx.File.Bytes) {
					body := cap.Node.ChildByFieldName("body")
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
					inner := stripStringDelimiters(tsutil.NodeText(expr, ctx.File.Bytes))
					if strings.TrimSpace(inner) == "" {
						tsutil.ReportNode(ctx, r, expr, "empty or whitespace-only docstring")
					}
				}
			}),

		core.NewRule("py/percent-format", "%-style string formatting", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range percentFormatQ.Run(ctx.Tree, ctx.File.Bytes) {
					tsutil.ReportNode(ctx, r, cap.Node, "%-style formatting; prefer f-strings or str.format()")
				}
			}),

		core.NewRule("py/redundant-parens-return", "redundant parentheses in return statement", core.Info,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range redundantParensQ.Run(ctx.Tree, ctx.File.Bytes) {
					tsutil.ReportNode(ctx, r, cap.Node, "redundant parentheses in return statement")
				}
			}),

		core.NewRule("py/assert-tuple", "assert with a tuple is always True", core.Error,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range assertTupleQ.Run(ctx.Tree, ctx.File.Bytes) {
					tsutil.ReportNode(ctx, r, cap.Node, "assert with a tuple is always True; did you mean 'assert cond, msg'?")
				}
			}),
	}
}

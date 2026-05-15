// Package javascript provides analyzers for JavaScript and TypeScript.
// JSAnalyzer handles .js/.jsx files; TSAnalyzer handles .ts/.tsx files and
// adds TypeScript-specific rules on top of the shared JS rule set.
package javascript

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"eastwood/core"
	"eastwood/tsutil"

	sitter "github.com/smacker/go-tree-sitter"
	sitterjs "github.com/smacker/go-tree-sitter/javascript"
	sitterts "github.com/smacker/go-tree-sitter/typescript/typescript"
	sittertsx "github.com/smacker/go-tree-sitter/typescript/tsx"
)

var (
	jsLang  = sitterjs.GetLanguage()
	tsLang  = sitterts.GetLanguage()
	tsxLang = sittertsx.GetLanguage()
)

// JSAnalyzer handles JavaScript (.js, .jsx, .mjs, .cjs) files.
type JSAnalyzer struct{}

func (JSAnalyzer) Language() string     { return "javascript" }
func (JSAnalyzer) Extensions() []string { return []string{".js", ".jsx", ".mjs", ".cjs"} }
func (JSAnalyzer) Parse(src []byte, _ string) (*sitter.Tree, error) {
	return parse(src, jsLang, "javascript")
}
func (JSAnalyzer) CommentRanges(src []byte, tree *sitter.Tree) []core.ByteRange {
	return jsCommentRanges(tree)
}
func (JSAnalyzer) Rules() []core.Rule { return jsRules(jsLang) }

// TSAnalyzer handles TypeScript (.ts, .tsx, .mts, .cts) files.
type TSAnalyzer struct{}

func (TSAnalyzer) Language() string     { return "typescript" }
func (TSAnalyzer) Extensions() []string { return []string{".ts", ".tsx", ".mts", ".cts"} }
func (TSAnalyzer) Parse(src []byte, path string) (*sitter.Tree, error) {
	lang := tsLang
	if strings.ToLower(filepath.Ext(path)) == ".tsx" {
		lang = tsxLang
	}
	return parse(src, lang, "typescript")
}
func (TSAnalyzer) CommentRanges(src []byte, tree *sitter.Tree) []core.ByteRange {
	return jsCommentRanges(tree)
}
func (TSAnalyzer) Rules() []core.Rule {
	// TS gets all JS rules + TS-specific rules.
	// We pass tsLang to the shared rules so queries work against TS trees.
	return append(jsRules(tsLang), tsOnlyRules(tsLang)...)
}

func parse(src []byte, lang *sitter.Language, name string) (*sitter.Tree, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(lang)
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, fmt.Errorf("%s parse: %w", name, err)
	}
	return tree, nil
}

func jsCommentRanges(tree *sitter.Tree) []core.ByteRange {
	if tree == nil {
		return nil
	}
	var ranges []core.ByteRange
	ranges = append(ranges, tsutil.CommentRangesFromTree(tree, "comment")...)
	ranges = append(ranges, tsutil.CommentRangesFromTree(tree, "hash_bang_line")...)
	return ranges
}

func nodeText(node *sitter.Node, src []byte) string {
	return string(src[node.StartByte():node.EndByte()])
}

func hasAncestor(node *sitter.Node, types ...string) bool {
	typeSet := make(map[string]bool, len(types))
	for _, t := range types {
		typeSet[t] = true
	}
	cur := node.Parent()
	for cur != nil {
		if typeSet[cur.Type()] {
			return true
		}
		cur = cur.Parent()
	}
	return false
}

// jsRules returns the shared JS/TS rules bound to the given language instance.
func jsRules(l *sitter.Language) []core.Rule {
	return []core.Rule{
		jsTripleEquality{lang: l},
		jsNoVar{lang: l},
		jsConsole{lang: l},
		jsDebugger{lang: l},
		jsNoThrowLiteral{lang: l},
		jsAwaitInLoop{lang: l},
		jsTemplateNoExpression{lang: l},
		jsTodoComment{lang: l},
	}
}

func tsOnlyRules(l *sitter.Language) []core.Rule {
	return []core.Rule{
		tsNoExplicitAny{lang: l},
		tsNonNullAssertion{lang: l},
	}
}

// --- rule: js/triple-equality ---

type jsTripleEquality struct{ lang *sitter.Language }

const tripleEqQuery = `(binary_expression operator: ["==" "!="] @op) @expr`

func (r jsTripleEquality) ID() string               { return "js/triple-equality" }
func (r jsTripleEquality) Description() string      { return "== or != instead of === / !==" }
func (r jsTripleEquality) DefaultSeverity() core.Severity { return core.Warning }

func (r jsTripleEquality) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, tripleEqQuery, r.lang) {
		if cap.Name == "op" {
			op := nodeText(cap.Node, ctx.File.Bytes)
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  fmt.Sprintf("use %s= instead of %s (strict equality avoids type coercion)", op, op),
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: js/no-var ---

type jsNoVar struct{ lang *sitter.Language }

const noVarQuery = `(variable_declaration) @decl`

func (r jsNoVar) ID() string               { return "js/no-var" }
func (r jsNoVar) Description() string      { return "var declaration; use let or const" }
func (r jsNoVar) DefaultSeverity() core.Severity { return core.Warning }

func (r jsNoVar) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, noVarQuery, r.lang) {
		// variable_declaration is only used for `var`; let/const use lexical_declaration.
		ctx.Report(core.Diagnostic{
			RuleID:   r.ID(),
			Severity: r.DefaultSeverity(),
			Message:  "var is function-scoped and hoisted; use let or const instead",
			Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
		})
	}
}

// --- rule: js/console ---

type jsConsole struct{ lang *sitter.Language }

const consoleQuery = `
(call_expression
  function: (member_expression
    object: (identifier) @obj
    property: (property_identifier) @prop)) @call
`

func (r jsConsole) ID() string               { return "js/console" }
func (r jsConsole) Description() string      { return "console.log/warn/error left in code" }
func (r jsConsole) DefaultSeverity() core.Severity { return core.Warning }

var consoleMethods = map[string]bool{
	"log": true, "warn": true, "error": true,
	"info": true, "debug": true, "trace": true,
}

func (r jsConsole) Check(ctx *core.RunContext) {
	seen := map[uint32]bool{}
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, consoleQuery, r.lang) {
		if cap.Name != "call" || seen[cap.Node.StartByte()] {
			continue
		}
		fn := cap.Node.ChildByFieldName("function")
		if fn == nil {
			continue
		}
		obj := fn.ChildByFieldName("object")
		prop := fn.ChildByFieldName("property")
		if obj == nil || prop == nil {
			continue
		}
		if nodeText(obj, ctx.File.Bytes) == "console" && consoleMethods[nodeText(prop, ctx.File.Bytes)] {
			seen[cap.Node.StartByte()] = true
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "console." + nodeText(prop, ctx.File.Bytes) + "() left in code; use a structured logger",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: js/debugger ---

type jsDebugger struct{ lang *sitter.Language }

const debuggerQuery = `(debugger_statement) @dbg`

func (r jsDebugger) ID() string               { return "js/debugger" }
func (r jsDebugger) Description() string      { return "debugger statement left in code" }
func (r jsDebugger) DefaultSeverity() core.Severity { return core.Error }

func (r jsDebugger) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, debuggerQuery, r.lang) {
		ctx.Report(core.Diagnostic{
			RuleID:   r.ID(),
			Severity: r.DefaultSeverity(),
			Message:  "debugger statement must be removed before shipping",
			Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
		})
	}
}

// --- rule: js/no-throw-literal ---

type jsNoThrowLiteral struct{ lang *sitter.Language }

const throwLiteralQuery = `
(throw_statement
  [(string) (number) (true) (false) (null) (undefined) (template_string)] @literal) @throw
`

func (r jsNoThrowLiteral) ID() string               { return "js/no-throw-literal" }
func (r jsNoThrowLiteral) Description() string      { return "throwing a non-Error value" }
func (r jsNoThrowLiteral) DefaultSeverity() core.Severity { return core.Warning }

func (r jsNoThrowLiteral) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, throwLiteralQuery, r.lang) {
		if cap.Name == "throw" {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "throw a proper Error object (new Error(...)) instead of a literal value",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: js/await-in-loop ---

type jsAwaitInLoop struct{ lang *sitter.Language }

const awaitQuery = `(await_expression) @await`

func (r jsAwaitInLoop) ID() string               { return "js/await-in-loop" }
func (r jsAwaitInLoop) Description() string      { return "await inside a loop; consider Promise.all()" }
func (r jsAwaitInLoop) DefaultSeverity() core.Severity { return core.Warning }

var loopTypes = []string{
	"for_statement", "for_in_statement", "for_of_statement",
	"while_statement", "do_statement",
}

func (r jsAwaitInLoop) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, awaitQuery, r.lang) {
		if hasAncestor(cap.Node, loopTypes...) {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "await inside a loop serialises async work; use Promise.all() for parallel execution",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: js/template-no-expression ---

type jsTemplateNoExpression struct{ lang *sitter.Language }

const templateQuery = `(template_string) @tmpl`

func (r jsTemplateNoExpression) ID() string               { return "js/template-no-expression" }
func (r jsTemplateNoExpression) Description() string      { return "template literal without any ${...} expression" }
func (r jsTemplateNoExpression) DefaultSeverity() core.Severity { return core.Warning }

func (r jsTemplateNoExpression) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, templateQuery, r.lang) {
		hasExpr := false
		for i := 0; i < int(cap.Node.NamedChildCount()); i++ {
			if cap.Node.NamedChild(i).Type() == "template_substitution" {
				hasExpr = true
				break
			}
		}
		if !hasExpr {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "template literal has no ${...} expression; use a plain string instead",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: js/todo-comment ---

type jsTodoComment struct{ lang *sitter.Language }

const jsCommentQuery = `(comment) @c`

func (r jsTodoComment) ID() string               { return "js/todo-comment" }
func (r jsTodoComment) Description() string      { return "TODO/FIXME/HACK comment left in code" }
func (r jsTodoComment) DefaultSeverity() core.Severity { return core.Info }

func (r jsTodoComment) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, jsCommentQuery, r.lang) {
		text := strings.ToUpper(nodeText(cap.Node, ctx.File.Bytes))
		var kind string
		switch {
		case strings.Contains(text, "TODO"):
			kind = "TODO"
		case strings.Contains(text, "FIXME"):
			kind = "FIXME"
		case strings.Contains(text, "HACK"):
			kind = "HACK"
		}
		if kind != "" {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  kind + " comment; track in your issue tracker",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: ts/no-explicit-any ---

type tsNoExplicitAny struct{ lang *sitter.Language }

const anyTypeQuery = `(predefined_type) @t`

func (r tsNoExplicitAny) ID() string               { return "ts/no-explicit-any" }
func (r tsNoExplicitAny) Description() string      { return "explicit 'any' type annotation" }
func (r tsNoExplicitAny) DefaultSeverity() core.Severity { return core.Warning }

func (r tsNoExplicitAny) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, anyTypeQuery, r.lang) {
		if nodeText(cap.Node, ctx.File.Bytes) == "any" {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "explicit 'any' disables type checking; use a specific type or 'unknown'",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: ts/non-null-assertion ---

type tsNonNullAssertion struct{ lang *sitter.Language }

const nonNullQuery = `(non_null_expression) @expr`

func (r tsNonNullAssertion) ID() string               { return "ts/non-null-assertion" }
func (r tsNonNullAssertion) Description() string      { return "! non-null assertion bypasses type safety" }
func (r tsNonNullAssertion) DefaultSeverity() core.Severity { return core.Warning }

func (r tsNonNullAssertion) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, nonNullQuery, r.lang) {
		ctx.Report(core.Diagnostic{
			RuleID:   r.ID(),
			Severity: r.DefaultSeverity(),
			Message:  "non-null assertion (!) bypasses null safety; add a proper null check instead",
			Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
		})
	}
}

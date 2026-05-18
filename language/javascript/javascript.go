// Package javascript provides analyzers for JavaScript and TypeScript.
// JSAnalyzer handles .js/.jsx files; TSAnalyzer handles .ts/.tsx files and
// adds TypeScript-specific rules on top of the shared JS rule set.
package javascript

import (
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

	jsPool  = tsutil.NewParserPool(jsLang)
	tsPool  = tsutil.NewParserPool(tsLang)
	tsxPool = tsutil.NewParserPool(tsxLang)
)

// JSAnalyzer handles JavaScript (.js, .jsx, .mjs, .cjs) files.
type JSAnalyzer struct{}

func (JSAnalyzer) Language() string     { return "javascript" }
func (JSAnalyzer) Extensions() []string { return []string{".js", ".jsx", ".mjs", ".cjs"} }
func (JSAnalyzer) Parse(src []byte, _ string) (*sitter.Tree, error) {
	tree, err := jsPool.ParseBytes(src)
	if err != nil {
		return nil, fmt.Errorf("javascript parse: %w", err)
	}
	return tree, nil
}
func (JSAnalyzer) CommentRanges(_ []byte, tree *sitter.Tree) []core.ByteRange {
	return jsCommentRanges(tree)
}
func (JSAnalyzer) Rules() []core.Rule { return jsRules(jsLang) }

// TSAnalyzer handles TypeScript (.ts, .tsx, .mts, .cts) files.
type TSAnalyzer struct{}

func (TSAnalyzer) Language() string     { return "typescript" }
func (TSAnalyzer) Extensions() []string { return []string{".ts", ".tsx", ".mts", ".cts"} }
func (TSAnalyzer) Parse(src []byte, path string) (*sitter.Tree, error) {
	p := tsPool
	if strings.ToLower(filepath.Ext(path)) == ".tsx" {
		p = tsxPool
	}
	tree, err := p.ParseBytes(src)
	if err != nil {
		return nil, fmt.Errorf("typescript parse: %w", err)
	}
	return tree, nil
}
func (TSAnalyzer) CommentRanges(_ []byte, tree *sitter.Tree) []core.ByteRange {
	return jsCommentRanges(tree)
}
func (TSAnalyzer) Rules() []core.Rule {
	return append(jsRules(tsLang), tsOnlyRules(tsLang)...)
}

func jsCommentRanges(tree *sitter.Tree) []core.ByteRange {
	if tree == nil {
		return nil
	}
	return tsutil.CommentRangesFromTree(tree, "comment", "hash_bang_line")
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

// jsRules returns the shared JS/TS rules compiled for the given language.
func jsRules(l *sitter.Language) []core.Rule {
	return []core.Rule{
		jsTripleEquality{q: tsutil.MustQuery(`(binary_expression operator: ["==" "!="] @op) @expr`, l)},
		jsNoVar{q: tsutil.MustQuery(`(variable_declaration) @decl`, l)},
		jsConsole{q: tsutil.MustQuery(`
(call_expression
  function: (member_expression
    object: (identifier) @obj
    property: (property_identifier) @prop)) @call
`, l)},
		jsDebugger{q: tsutil.MustQuery(`(debugger_statement) @dbg`, l)},
		jsNoThrowLiteral{q: tsutil.MustQuery(`
(throw_statement
  [(string) (number) (true) (false) (null) (undefined) (template_string)] @literal) @throw
`, l)},
		jsAwaitInLoop{q: tsutil.MustQuery(`(await_expression) @await`, l)},
		jsTemplateNoExpression{q: tsutil.MustQuery(`(template_string) @tmpl`, l)},
		jsTodoComment{q: tsutil.MustQuery(`(comment) @c`, l)},
	}
}

func tsOnlyRules(l *sitter.Language) []core.Rule {
	return []core.Rule{
		tsNoExplicitAny{q: tsutil.MustQuery(`(predefined_type) @t`, l)},
		tsNonNullAssertion{q: tsutil.MustQuery(`(non_null_expression) @expr`, l)},
	}
}

// --- rule: js/triple-equality ---

type jsTripleEquality struct{ q tsutil.CompiledQuery }

func (r jsTripleEquality) ID() string                    { return "js/triple-equality" }
func (r jsTripleEquality) Description() string           { return "== or != instead of === / !==" }
func (r jsTripleEquality) DefaultSeverity() core.Severity { return core.Warning }

func (r jsTripleEquality) Check(ctx *core.RunContext) {
	for cap := range r.q.Run(ctx.Tree, ctx.File.Bytes) {
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

type jsNoVar struct{ q tsutil.CompiledQuery }

func (r jsNoVar) ID() string                    { return "js/no-var" }
func (r jsNoVar) Description() string           { return "var declaration; use let or const" }
func (r jsNoVar) DefaultSeverity() core.Severity { return core.Warning }

func (r jsNoVar) Check(ctx *core.RunContext) {
	for cap := range r.q.Run(ctx.Tree, ctx.File.Bytes) {
		ctx.Report(core.Diagnostic{
			RuleID:   r.ID(),
			Severity: r.DefaultSeverity(),
			Message:  "var is function-scoped and hoisted; use let or const instead",
			Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
		})
	}
}

// --- rule: js/console ---

type jsConsole struct{ q tsutil.CompiledQuery }

func (r jsConsole) ID() string                    { return "js/console" }
func (r jsConsole) Description() string           { return "console.log/warn/error left in code" }
func (r jsConsole) DefaultSeverity() core.Severity { return core.Warning }

var consoleMethods = map[string]bool{
	"log": true, "warn": true, "error": true,
	"info": true, "debug": true, "trace": true,
}

func (r jsConsole) Check(ctx *core.RunContext) {
	seen := map[uint32]bool{}
	for cap := range r.q.Run(ctx.Tree, ctx.File.Bytes) {
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

type jsDebugger struct{ q tsutil.CompiledQuery }

func (r jsDebugger) ID() string                    { return "js/debugger" }
func (r jsDebugger) Description() string           { return "debugger statement left in code" }
func (r jsDebugger) DefaultSeverity() core.Severity { return core.Error }

func (r jsDebugger) Check(ctx *core.RunContext) {
	for cap := range r.q.Run(ctx.Tree, ctx.File.Bytes) {
		ctx.Report(core.Diagnostic{
			RuleID:   r.ID(),
			Severity: r.DefaultSeverity(),
			Message:  "debugger statement must be removed before shipping",
			Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
		})
	}
}

// --- rule: js/no-throw-literal ---

type jsNoThrowLiteral struct{ q tsutil.CompiledQuery }

func (r jsNoThrowLiteral) ID() string                    { return "js/no-throw-literal" }
func (r jsNoThrowLiteral) Description() string           { return "throwing a non-Error value" }
func (r jsNoThrowLiteral) DefaultSeverity() core.Severity { return core.Warning }

func (r jsNoThrowLiteral) Check(ctx *core.RunContext) {
	for cap := range r.q.Run(ctx.Tree, ctx.File.Bytes) {
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

type jsAwaitInLoop struct{ q tsutil.CompiledQuery }

func (r jsAwaitInLoop) ID() string                    { return "js/await-in-loop" }
func (r jsAwaitInLoop) Description() string           { return "await inside a loop; consider Promise.all()" }
func (r jsAwaitInLoop) DefaultSeverity() core.Severity { return core.Warning }

var loopTypes = []string{
	"for_statement", "for_in_statement", "for_of_statement",
	"while_statement", "do_statement",
}

func (r jsAwaitInLoop) Check(ctx *core.RunContext) {
	for cap := range r.q.Run(ctx.Tree, ctx.File.Bytes) {
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

type jsTemplateNoExpression struct{ q tsutil.CompiledQuery }

func (r jsTemplateNoExpression) ID() string                    { return "js/template-no-expression" }
func (r jsTemplateNoExpression) Description() string           { return "template literal without any ${...} expression" }
func (r jsTemplateNoExpression) DefaultSeverity() core.Severity { return core.Warning }

func (r jsTemplateNoExpression) Check(ctx *core.RunContext) {
	for cap := range r.q.Run(ctx.Tree, ctx.File.Bytes) {
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

type jsTodoComment struct{ q tsutil.CompiledQuery }

func (r jsTodoComment) ID() string                    { return "js/todo-comment" }
func (r jsTodoComment) Description() string           { return "TODO/FIXME/HACK comment left in code" }
func (r jsTodoComment) DefaultSeverity() core.Severity { return core.Info }

func (r jsTodoComment) Check(ctx *core.RunContext) {
	for cap := range r.q.Run(ctx.Tree, ctx.File.Bytes) {
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

type tsNoExplicitAny struct{ q tsutil.CompiledQuery }

func (r tsNoExplicitAny) ID() string                    { return "ts/no-explicit-any" }
func (r tsNoExplicitAny) Description() string           { return "explicit 'any' type annotation" }
func (r tsNoExplicitAny) DefaultSeverity() core.Severity { return core.Warning }

func (r tsNoExplicitAny) Check(ctx *core.RunContext) {
	for cap := range r.q.Run(ctx.Tree, ctx.File.Bytes) {
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

type tsNonNullAssertion struct{ q tsutil.CompiledQuery }

func (r tsNonNullAssertion) ID() string                    { return "ts/non-null-assertion" }
func (r tsNonNullAssertion) Description() string           { return "! non-null assertion bypasses type safety" }
func (r tsNonNullAssertion) DefaultSeverity() core.Severity { return core.Warning }

func (r tsNonNullAssertion) Check(ctx *core.RunContext) {
	for cap := range r.q.Run(ctx.Tree, ctx.File.Bytes) {
		ctx.Report(core.Diagnostic{
			RuleID:   r.ID(),
			Severity: r.DefaultSeverity(),
			Message:  "non-null assertion (!) bypasses null safety; add a proper null check instead",
			Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
		})
	}
}

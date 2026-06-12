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
	sittertsx "github.com/smacker/go-tree-sitter/typescript/tsx"
	sitterts "github.com/smacker/go-tree-sitter/typescript/typescript"
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
func (JSAnalyzer) Rules() []core.Rule { return append(jsRules(jsLang), styleRules(jsLang)...) }

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
	rules := append(jsRules(tsLang), styleRules(tsLang)...)
	return append(rules, tsOnlyRules(tsLang)...)
}

func jsCommentRanges(tree *sitter.Tree) []core.ByteRange {
	if tree == nil {
		return nil
	}
	return tsutil.CommentRangesFromTree(tree, "comment", "hash_bang_line")
}

var loopTypes = []string{
	"for_statement", "for_in_statement", "for_of_statement",
	"while_statement", "do_statement",
}

var alertFns = map[string]bool{"alert": true, "confirm": true, "prompt": true}

var consoleMethods = map[string]bool{
	"log": true, "warn": true, "error": true,
	"info": true, "debug": true, "trace": true,
}

var wrapperConstructors = map[string]bool{
	"Boolean": true, "Number": true, "String": true, "Symbol": true, "BigInt": true,
}

// jsRules returns the shared JS/TS rules compiled for the given grammar.
func jsRules(l *sitter.Language) []core.Rule {
	var (
		binaryOpQ   = tsutil.MustQuery(`(binary_expression operator: ["==" "!="] @op) @expr`, l)
		varDeclQ    = tsutil.MustQuery(`(variable_declaration) @decl`, l)
		memberCallQ = tsutil.MustQuery(`
(call_expression
  function: (member_expression
    object: (identifier) @obj
    property: (property_identifier) @prop)) @call
`, l)
		debuggerQ = tsutil.MustQuery(`(debugger_statement) @dbg`, l)
		throwLitQ = tsutil.MustQuery(`
(throw_statement
  [(string) (number) (true) (false) (null) (undefined) (template_string)] @literal) @throw
`, l)
		awaitQ     = tsutil.MustQuery(`(await_expression) @await`, l)
		templateQ  = tsutil.MustQuery(`(template_string) @tmpl`, l)
		identCallQ = tsutil.MustQuery(`(call_expression function: (identifier) @fn) @call`, l)
		newExprQ   = tsutil.MustQuery(`(new_expression constructor: (identifier) @name) @expr`, l)
	)

	return []core.Rule{
		core.NewRule("js/triple-equality", "== or != instead of === / !==", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range binaryOpQ.Run(ctx.Tree, ctx.File.Bytes) {
					if cap.Name == "op" {
						op := tsutil.NodeText(cap.Node, ctx.File.Bytes)
						tsutil.ReportNode(ctx, r, cap.Node,
							fmt.Sprintf("use %s= instead of %s (strict equality avoids type coercion)", op, op))
					}
				}
			}),

		core.NewRule("js/no-var", "var declaration; use let or const", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range varDeclQ.Run(ctx.Tree, ctx.File.Bytes) {
					tsutil.ReportNode(ctx, r, cap.Node, "var is function-scoped and hoisted; use let or const instead")
				}
			}),

		core.NewRule("js/console", "console.log/warn/error left in code", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				src := ctx.File.Bytes
				for m := range memberCallQ.Matches(ctx.Tree, src) {
					obj, prop := m.Node("obj"), m.Node("prop")
					if obj == nil || prop == nil {
						continue
					}
					if tsutil.NodeText(obj, src) == "console" && consoleMethods[tsutil.NodeText(prop, src)] {
						tsutil.ReportNode(ctx, r, m.Node("call"),
							"console."+tsutil.NodeText(prop, src)+"() left in code; use a structured logger")
					}
				}
			}),

		core.NewRule("js/debugger", "debugger statement left in code", core.Error,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range debuggerQ.Run(ctx.Tree, ctx.File.Bytes) {
					tsutil.ReportNode(ctx, r, cap.Node, "debugger statement must be removed before shipping")
				}
			}),

		core.NewRule("js/no-throw-literal", "throwing a non-Error value", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range throwLitQ.Run(ctx.Tree, ctx.File.Bytes) {
					if cap.Name == "throw" {
						tsutil.ReportNode(ctx, r, cap.Node, "throw a proper Error object (new Error(...)) instead of a literal value")
					}
				}
			}),

		core.NewRule("js/await-in-loop", "await inside a loop; consider Promise.all()", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range awaitQ.Run(ctx.Tree, ctx.File.Bytes) {
					if tsutil.HasAncestor(cap.Node, loopTypes...) {
						tsutil.ReportNode(ctx, r, cap.Node,
							"await inside a loop serialises async work; use Promise.all() for parallel execution")
					}
				}
			}),

		core.NewRule("js/template-no-expression", "template literal without any ${...} expression", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range templateQ.Run(ctx.Tree, ctx.File.Bytes) {
					hasExpr := false
					for i := 0; i < int(cap.Node.NamedChildCount()); i++ {
						if cap.Node.NamedChild(i).Type() == "template_substitution" {
							hasExpr = true
							break
						}
					}
					if !hasExpr {
						tsutil.ReportNode(ctx, r, cap.Node, "template literal has no ${...} expression; use a plain string instead")
					}
				}
			}),

		tsutil.TodoCommentRule("js/todo-comment", l,
			[]string{"TODO", "FIXME", "HACK"}, "; track in your issue tracker", "comment"),

		core.NewRule("js/no-eval", "eval() call is a security risk", core.Error,
			func(r core.Rule, ctx *core.RunContext) {
				for m := range identCallQ.Matches(ctx.Tree, ctx.File.Bytes) {
					if fn := m.Node("fn"); fn != nil && tsutil.NodeText(fn, ctx.File.Bytes) == "eval" {
						tsutil.ReportNode(ctx, r, m.Node("call"),
							"eval() executes arbitrary code; use JSON.parse() or a safer alternative")
					}
				}
			}),

		core.NewRule("js/no-alert", "alert/confirm/prompt left in code", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for m := range identCallQ.Matches(ctx.Tree, ctx.File.Bytes) {
					fn := m.Node("fn")
					if fn != nil && alertFns[tsutil.NodeText(fn, ctx.File.Bytes)] {
						tsutil.ReportNode(ctx, r, m.Node("call"),
							tsutil.NodeText(fn, ctx.File.Bytes)+"() is a browser blocking call; remove before shipping")
					}
				}
			}),

		core.NewRule("js/no-new-wrapper", "new Boolean/Number/String wrapper object", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for m := range newExprQ.Matches(ctx.Tree, ctx.File.Bytes) {
					ctor := m.Node("name")
					if ctor != nil && wrapperConstructors[tsutil.NodeText(ctor, ctx.File.Bytes)] {
						tsutil.ReportNode(ctx, r, m.Node("expr"),
							fmt.Sprintf("new %s() creates an object wrapper; use the primitive literal instead", tsutil.NodeText(ctor, ctx.File.Bytes)))
					}
				}
			}),
	}
}

func tsOnlyRules(l *sitter.Language) []core.Rule {
	var (
		predefinedTypeQ = tsutil.MustQuery(`(predefined_type) @t`, l)
		nonNullQ        = tsutil.MustQuery(`(non_null_expression) @expr`, l)
		namespaceQ      = tsutil.MustQuery(`(internal_module) @ns`, l)
	)

	return []core.Rule{
		core.NewRule("ts/no-explicit-any", "explicit 'any' type annotation", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range predefinedTypeQ.Run(ctx.Tree, ctx.File.Bytes) {
					if tsutil.NodeText(cap.Node, ctx.File.Bytes) == "any" {
						tsutil.ReportNode(ctx, r, cap.Node, "explicit 'any' disables type checking; use a specific type or 'unknown'")
					}
				}
			}),

		core.NewRule("ts/non-null-assertion", "! non-null assertion bypasses type safety", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range nonNullQ.Run(ctx.Tree, ctx.File.Bytes) {
					tsutil.ReportNode(ctx, r, cap.Node, "non-null assertion (!) bypasses null safety; add a proper null check instead")
				}
			}),

		core.NewRule("ts/no-namespace", "TypeScript namespace/module declaration", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range namespaceQ.Run(ctx.Tree, ctx.File.Bytes) {
					tsutil.ReportNode(ctx, r, cap.Node, "namespace/module declarations are discouraged; use ES modules (import/export) instead")
				}
			}),
	}
}

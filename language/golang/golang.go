// Package golang provides the Go language analyzer and all built-in rules.
package golang

import (
	"fmt"
	"strings"

	"eastwood/core"
	"eastwood/tsutil"

	sitter "github.com/smacker/go-tree-sitter"
	sittergo "github.com/smacker/go-tree-sitter/golang"
)

// Analyzer implements core.Analyzer for Go source files.
type Analyzer struct{}

func (Analyzer) Language() string     { return "go" }
func (Analyzer) Extensions() []string { return []string{".go"} }

func (Analyzer) Parse(src []byte, _ string) (*sitter.Tree, error) {
	tree, err := pool.ParseBytes(src)
	if err != nil {
		return nil, fmt.Errorf("go parse: %w", err)
	}
	return tree, nil
}

func (Analyzer) CommentRanges(_ []byte, tree *sitter.Tree) []core.ByteRange {
	if tree == nil {
		return nil
	}
	return tsutil.CommentRangesFromTree(tree, "comment")
}

var (
	lang = sittergo.GetLanguage()
	pool = tsutil.NewParserPool(lang)

	emptyIfaceQ = tsutil.MustQuery(`(interface_type) @iface`, lang)
	callQ       = tsutil.MustQuery(`(call_expression function: (identifier) @fn) @call`, lang)
	errofQ      = tsutil.MustQuery(`
(call_expression
  function: (selector_expression
    operand: (identifier) @pkg
    field: (field_identifier) @fn)
  arguments: (argument_list
    [(interpreted_string_literal) (raw_string_literal)] @fmt)) @call
`, lang)
	deferQ         = tsutil.MustQuery(`(defer_statement) @def`, lang)
	commentQ       = tsutil.MustQuery(`(comment) @c`, lang)
	returnQ        = tsutil.MustQuery(`(return_statement) @ret`, lang)
	goroutineAnonQ = tsutil.MustQuery(`
(go_statement
  (call_expression
    function: (func_literal))) @goroutine
`, lang)
	selectorCallQ = tsutil.MustQuery(`
(call_expression
  function: (selector_expression
    operand: (identifier) @pkg
    field: (field_identifier) @fn)) @call
`, lang)
	contextStructQ = tsutil.MustQuery(`
(field_declaration
  type: (qualified_type
    package: (package_identifier) @pkg
    name: (type_identifier) @name)) @field
`, lang)
)

// selectorCallRule flags calls of the form pkg.Method for methods in the set.
func selectorCallRule(id, desc string, sev core.Severity, pkg string, methods map[string]bool, msgf func(method string) string) core.Rule {
	return core.NewRule(id, desc, sev, func(r core.Rule, ctx *core.RunContext) {
		src := ctx.File.Bytes
		for m := range selectorCallQ.Matches(ctx.Tree, src) {
			pkgN, fnN := m.Node("pkg"), m.Node("fn")
			if pkgN == nil || fnN == nil || tsutil.NodeText(pkgN, src) != pkg {
				continue
			}
			method := tsutil.NodeText(fnN, src)
			if methods[method] {
				tsutil.ReportNode(ctx, r, m.Node("call"), msgf(method))
			}
		}
	})
}

func (Analyzer) Rules() []core.Rule {
	return []core.Rule{
		core.NewRule("go/empty-interface", "interface{} usage; use 'any' instead (Go 1.18+)", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range emptyIfaceQ.Run(ctx.Tree, ctx.File.Bytes) {
					if cap.Node.NamedChildCount() == 0 {
						tsutil.ReportNode(ctx, r, cap.Node, "use 'any' instead of 'interface{}'")
					}
				}
			}),

		core.NewRule("go/panic", "panic() call", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range callQ.Run(ctx.Tree, ctx.File.Bytes) {
					if cap.Name == "fn" && tsutil.NodeText(cap.Node, ctx.File.Bytes) == "panic" {
						tsutil.ReportNode(ctx, r, cap.Node, "panic() in non-test code; prefer returning an error")
					}
				}
			}),

		core.NewRule("go/errorf-no-wrap", "fmt.Errorf without %w loses error wrapping", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				src := ctx.File.Bytes
				for m := range errofQ.Matches(ctx.Tree, src) {
					pkgN, fnN, fmtN := m.Node("pkg"), m.Node("fn"), m.Node("fmt")
					if pkgN == nil || fnN == nil || fmtN == nil {
						continue
					}
					if tsutil.NodeText(pkgN, src) != "fmt" || tsutil.NodeText(fnN, src) != "Errorf" {
						continue
					}
					call := m.Node("call")
					args := call.ChildByFieldName("arguments")
					if args == nil || args.NamedChildCount() < 2 || !fmtN.Equal(args.NamedChild(0)) {
						continue
					}
					fmtStr := strings.Trim(tsutil.NodeText(fmtN, src), "`\"")
					if (strings.Contains(fmtStr, "%s") || strings.Contains(fmtStr, "%v")) &&
						!strings.Contains(fmtStr, "%w") {
						tsutil.ReportNode(ctx, r, call, "fmt.Errorf uses %s/%v for error; use %w to preserve the error chain")
					}
				}
			}),

		core.NewRule("go/defer-in-loop", "defer inside a loop may not execute when expected", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range deferQ.Run(ctx.Tree, ctx.File.Bytes) {
					if tsutil.HasAncestor(cap.Node, "for_statement") {
						tsutil.ReportNode(ctx, r, cap.Node, "defer inside a for loop runs at function exit, not loop iteration end")
					}
				}
			}),

		tsutil.TodoCommentRule("go/todo-comment", lang,
			[]string{"TODO", "FIXME", "HACK", "XXX"}, "; track in your issue tracker instead", "comment"),

		core.NewRule("go/naked-return", "bare return statement in named-result function", core.Info,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range returnQ.Run(ctx.Tree, ctx.File.Bytes) {
					if cap.Node.NamedChildCount() == 0 {
						tsutil.ReportNode(ctx, r, cap.Node, "naked return; explicitly return values for clarity")
					}
				}
			}),

		core.NewRule("go/goroutine-anon", "anonymous goroutine; easy to leak and hard to trace", core.Info,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range goroutineAnonQ.Run(ctx.Tree, ctx.File.Bytes) {
					tsutil.ReportNode(ctx, r, cap.Node, "anonymous goroutine; consider a named function and ensure the goroutine is properly waited for")
				}
			}),

		core.NewRule("go/build-tag-old", "old-style //go:build constraint", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range commentQ.Run(ctx.Tree, ctx.File.Bytes) {
					if strings.HasPrefix(tsutil.NodeText(cap.Node, ctx.File.Bytes), "// +build ") {
						tsutil.ReportNode(ctx, r, cap.Node, "use //go:build instead of // +build (deprecated since Go 1.17)")
					}
				}
			}),

		selectorCallRule("go/print", "fmt.Print/Println/Printf left in code", core.Info,
			"fmt", map[string]bool{"Print": true, "Println": true, "Printf": true},
			func(method string) string {
				return fmt.Sprintf("fmt.%s left in code; use a structured logger", method)
			}),

		selectorCallRule("go/os-exit", "os.Exit call skips deferred functions", core.Warning,
			"os", map[string]bool{"Exit": true},
			func(string) string {
				return "os.Exit skips all deferred calls; only use in main() or TestMain()"
			}),

		selectorCallRule("go/log-fatal", "log.Fatal/Fatalf/Fatalln skips deferred functions", core.Warning,
			"log", map[string]bool{"Fatal": true, "Fatalf": true, "Fatalln": true},
			func(method string) string {
				return fmt.Sprintf("log.%s skips all deferred calls; use log.Print+return or a fatal error handler", method)
			}),

		core.NewRule("go/context-in-struct", "context.Context stored as a struct field", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				src := ctx.File.Bytes
				for m := range contextStructQ.Matches(ctx.Tree, src) {
					if tsutil.NodeText(m.Node("pkg"), src) == "context" &&
						tsutil.NodeText(m.Node("name"), src) == "Context" {
						tsutil.ReportNode(ctx, r, m.Node("field"),
							"context.Context should not be stored in a struct; pass it as a function parameter instead")
					}
				}
			}),
	}
}

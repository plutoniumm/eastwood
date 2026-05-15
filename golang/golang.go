// Package golang provides the Go language analyzer and all built-in rules.
package golang

import (
	"context"
	"fmt"
	"strings"

	"eastwood/core"
	"eastwood/tsutil"

	sitter "github.com/smacker/go-tree-sitter"
	sittergo "github.com/smacker/go-tree-sitter/golang"
)

// Analyzer implements core.Analyzer for Go source files.
type Analyzer struct{}

func (Analyzer) Language() string          { return "go" }
func (Analyzer) Extensions() []string      { return []string{".go"} }
func (Analyzer) Parse(src []byte, _ string) (*sitter.Tree, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(sittergo.GetLanguage())
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, fmt.Errorf("go parse: %w", err)
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
		goEmptyInterface{},
		goPanic{},
		goErrofNoWrap{},
		goDeferInLoop{},
		goTodoComment{},
		goNakedReturn{},
		goGoroutineAnon{},
		goBuildTagOld{},
		goPrint{},
		goOsExit{},
	}
}

var lang = sittergo.GetLanguage()

func nodeText(node *sitter.Node, src []byte) string {
	return string(src[node.StartByte():node.EndByte()])
}

// hasAncestor reports whether any ancestor of node has the given type.
func hasAncestor(node *sitter.Node, typeName string) bool {
	cur := node.Parent()
	for cur != nil {
		if cur.Type() == typeName {
			return true
		}
		cur = cur.Parent()
	}
	return false
}

// --- rule: go/empty-interface ---

type goEmptyInterface struct{}

const emptyIfaceQuery = `(interface_type) @iface`

func (goEmptyInterface) ID() string               { return "go/empty-interface" }
func (goEmptyInterface) Description() string      { return "interface{} usage; use 'any' instead (Go 1.18+)" }
func (goEmptyInterface) DefaultSeverity() core.Severity { return core.Warning }

func (r goEmptyInterface) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, emptyIfaceQuery, lang) {
		if cap.Node.NamedChildCount() == 0 {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "use 'any' instead of 'interface{}'",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: go/panic ---

type goPanic struct{}

const goPanicQuery = `(call_expression function: (identifier) @fn) @call`

func (goPanic) ID() string               { return "go/panic" }
func (goPanic) Description() string      { return "panic() call" }
func (goPanic) DefaultSeverity() core.Severity { return core.Warning }

func (r goPanic) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, goPanicQuery, lang) {
		if cap.Name == "fn" && nodeText(cap.Node, ctx.File.Bytes) == "panic" {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "panic() in non-test code; prefer returning an error",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: go/errorf-no-wrap ---

type goErrofNoWrap struct{}

const errofQuery = `
(call_expression
  function: (selector_expression
    operand: (identifier) @pkg
    field: (field_identifier) @fn)
  arguments: (argument_list
    [(interpreted_string_literal) (raw_string_literal)] @fmt)) @call
`

func (goErrofNoWrap) ID() string               { return "go/errorf-no-wrap" }
func (goErrofNoWrap) Description() string      { return "fmt.Errorf without %w loses error wrapping" }
func (goErrofNoWrap) DefaultSeverity() core.Severity { return core.Warning }

func (r goErrofNoWrap) Check(ctx *core.RunContext) {
	seen := map[uint32]bool{} // track call nodes already reported
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, errofQuery, lang) {
		if cap.Name != "call" {
			continue
		}
		call := cap.Node
		if seen[call.StartByte()] {
			continue
		}
		pkgNode := call.ChildByFieldName("function")
		if pkgNode == nil {
			continue
		}
		// Extract pkg and fn from selector_expression children.
		pkg, fn := selectorParts(pkgNode, ctx.File.Bytes)
		if pkg != "fmt" || fn != "Errorf" {
			continue
		}
		// Get format string (first argument).
		args := call.ChildByFieldName("arguments")
		if args == nil || args.NamedChildCount() == 0 {
			continue
		}
		fmtStr := nodeText(args.NamedChild(0), ctx.File.Bytes)
		fmtStr = strings.Trim(fmtStr, "`\"")
		if (strings.Contains(fmtStr, "%s") || strings.Contains(fmtStr, "%v")) &&
			!strings.Contains(fmtStr, "%w") &&
			args.NamedChildCount() > 1 {
			seen[call.StartByte()] = true
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "fmt.Errorf uses %s/%v for error; use %w to preserve the error chain",
				Range:    tsutil.NodeRange(call, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

func selectorParts(node *sitter.Node, src []byte) (pkg, field string) {
	if node.Type() != "selector_expression" {
		return
	}
	if op := node.ChildByFieldName("operand"); op != nil {
		pkg = nodeText(op, src)
	}
	if f := node.ChildByFieldName("field"); f != nil {
		field = nodeText(f, src)
	}
	return
}

// --- rule: go/defer-in-loop ---

type goDeferInLoop struct{}

const deferQuery = `(defer_statement) @def`

func (goDeferInLoop) ID() string               { return "go/defer-in-loop" }
func (goDeferInLoop) Description() string      { return "defer inside a loop may not execute when expected" }
func (goDeferInLoop) DefaultSeverity() core.Severity { return core.Warning }

func (r goDeferInLoop) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, deferQuery, lang) {
		if hasAncestor(cap.Node, "for_statement") {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "defer inside a for loop runs at function exit, not loop iteration end",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: go/todo-comment ---

type goTodoComment struct{}

const goCommentQuery = `(comment) @c`

func (goTodoComment) ID() string               { return "go/todo-comment" }
func (goTodoComment) Description() string      { return "TODO/FIXME/HACK comment left in code" }
func (goTodoComment) DefaultSeverity() core.Severity { return core.Info }

func (r goTodoComment) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, goCommentQuery, lang) {
		text := strings.ToUpper(nodeText(cap.Node, ctx.File.Bytes))
		var kind string
		switch {
		case strings.Contains(text, "TODO"):
			kind = "TODO"
		case strings.Contains(text, "FIXME"):
			kind = "FIXME"
		case strings.Contains(text, "HACK"):
			kind = "HACK"
		case strings.Contains(text, "XXX"):
			kind = "XXX"
		}
		if kind != "" {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  kind + " comment; track in your issue tracker instead",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: go/naked-return ---

type goNakedReturn struct{}

const returnQuery = `(return_statement) @ret`

func (goNakedReturn) ID() string               { return "go/naked-return" }
func (goNakedReturn) Description() string      { return "bare return statement in named-result function" }
func (goNakedReturn) DefaultSeverity() core.Severity { return core.Info }

func (r goNakedReturn) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, returnQuery, lang) {
		if cap.Node.NamedChildCount() == 0 {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "naked return; explicitly return values for clarity",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: go/goroutine-anon ---

type goGoroutineAnon struct{}

const goroutineAnonQuery = `
(go_statement
  (call_expression
    function: (func_literal))) @goroutine
`

func (goGoroutineAnon) ID() string               { return "go/goroutine-anon" }
func (goGoroutineAnon) Description() string      { return "anonymous goroutine; easy to leak and hard to trace" }
func (goGoroutineAnon) DefaultSeverity() core.Severity { return core.Info }

func (r goGoroutineAnon) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, goroutineAnonQuery, lang) {
		ctx.Report(core.Diagnostic{
			RuleID:   r.ID(),
			Severity: r.DefaultSeverity(),
			Message:  "anonymous goroutine; consider a named function and ensure the goroutine is properly waited for",
			Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
		})
	}
}

// --- rule: go/build-tag-old ---

type goBuildTagOld struct{}

func (goBuildTagOld) ID() string               { return "go/build-tag-old" }
func (goBuildTagOld) Description() string      { return "old-style //go:build constraint" }
func (goBuildTagOld) DefaultSeverity() core.Severity { return core.Warning }

func (r goBuildTagOld) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, goCommentQuery, lang) {
		text := nodeText(cap.Node, ctx.File.Bytes)
		if strings.HasPrefix(text, "// +build ") {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "use //go:build instead of // +build (deprecated since Go 1.17)",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: go/print ---

type goPrint struct{}

const printQuery = `
(call_expression
  function: (selector_expression
    operand: (identifier) @pkg
    field: (field_identifier) @fn)) @call
`

func (goPrint) ID() string               { return "go/print" }
func (goPrint) Description() string      { return "fmt.Print/Println/Printf left in code" }
func (goPrint) DefaultSeverity() core.Severity { return core.Info }

func (r goPrint) Check(ctx *core.RunContext) {
	seen := map[uint32]bool{}
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, printQuery, lang) {
		if cap.Name != "call" || seen[cap.Node.StartByte()] {
			continue
		}
		fn := cap.Node.ChildByFieldName("function")
		if fn == nil {
			continue
		}
		pkg, method := selectorParts(fn, ctx.File.Bytes)
		if pkg != "fmt" {
			continue
		}
		switch method {
		case "Print", "Println", "Printf":
			seen[cap.Node.StartByte()] = true
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  fmt.Sprintf("fmt.%s left in code; use a structured logger", method),
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: go/os-exit ---

type goOsExit struct{}

func (goOsExit) ID() string               { return "go/os-exit" }
func (goOsExit) Description() string      { return "os.Exit call skips deferred functions" }
func (goOsExit) DefaultSeverity() core.Severity { return core.Warning }

func (r goOsExit) Check(ctx *core.RunContext) {
	seen := map[uint32]bool{}
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, printQuery, lang) {
		if cap.Name != "call" || seen[cap.Node.StartByte()] {
			continue
		}
		fn := cap.Node.ChildByFieldName("function")
		if fn == nil {
			continue
		}
		pkg, method := selectorParts(fn, ctx.File.Bytes)
		if pkg == "os" && method == "Exit" {
			seen[cap.Node.StartByte()] = true
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "os.Exit skips all deferred calls; only use in main() or TestMain()",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

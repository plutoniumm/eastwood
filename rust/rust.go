// Package rust provides the Rust language analyzer and all built-in rules.
package rust

import (
	"context"
	"fmt"
	"strings"

	"eastwood/core"
	"eastwood/tsutil"

	sitter "github.com/smacker/go-tree-sitter"
	sitterrust "github.com/smacker/go-tree-sitter/rust"
)

// Analyzer implements core.Analyzer for Rust source files.
type Analyzer struct{}

func (Analyzer) Language() string     { return "rust" }
func (Analyzer) Extensions() []string { return []string{".rs"} }

func (Analyzer) Parse(src []byte, _ string) (*sitter.Tree, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(sitterrust.GetLanguage())
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, fmt.Errorf("rust parse: %w", err)
	}
	return tree, nil
}

func (Analyzer) CommentRanges(src []byte, tree *sitter.Tree) []core.ByteRange {
	if tree == nil {
		return nil
	}
	// Rust has line_comment and block_comment node types.
	var ranges []core.ByteRange
	ranges = append(ranges, tsutil.CommentRangesFromTree(tree, "line_comment")...)
	ranges = append(ranges, tsutil.CommentRangesFromTree(tree, "block_comment")...)
	return ranges
}

func (Analyzer) Rules() []core.Rule {
	return []core.Rule{
		rsUnwrap{},
		rsExpect{},
		rsPanic{},
		rsTodo{},
		rsUnimplemented{},
		rsDbg{},
		rsUnsafeBlock{},
		rsAllowAttribute{},
		rsClone{},
		rsPrint{},
	}
}

var lang = sitterrust.GetLanguage()

func nodeText(node *sitter.Node, src []byte) string {
	return string(src[node.StartByte():node.EndByte()])
}

// methodCallQuery matches method calls: expr.method(args)
const methodCallQuery = `
(call_expression
  function: (field_expression
    field: (field_identifier) @method)) @call
`

// macroQuery matches macro invocations: name!(...)
const macroQuery = `(macro_invocation macro: (identifier) @name) @mac`

// --- rule: rs/unwrap ---

type rsUnwrap struct{}

func (rsUnwrap) ID() string               { return "rs/unwrap" }
func (rsUnwrap) Description() string      { return ".unwrap() call; propagate errors with ? instead" }
func (rsUnwrap) DefaultSeverity() core.Severity { return core.Warning }

func (r rsUnwrap) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, methodCallQuery, lang) {
		if cap.Name == "method" && nodeText(cap.Node, ctx.File.Bytes) == "unwrap" {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  ".unwrap() panics on Err/None; propagate with ? or handle explicitly",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: rs/expect ---

type rsExpect struct{}

func (rsExpect) ID() string               { return "rs/expect" }
func (rsExpect) Description() string      { return ".expect() call; propagate errors with ? instead" }
func (rsExpect) DefaultSeverity() core.Severity { return core.Warning }

func (r rsExpect) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, methodCallQuery, lang) {
		if cap.Name == "method" && nodeText(cap.Node, ctx.File.Bytes) == "expect" {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  ".expect() panics on Err/None; propagate with ? or handle explicitly",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: rs/panic ---

type rsPanic struct{}

func (rsPanic) ID() string               { return "rs/panic" }
func (rsPanic) Description() string      { return "panic!() macro" }
func (rsPanic) DefaultSeverity() core.Severity { return core.Warning }

func (r rsPanic) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, macroQuery, lang) {
		if cap.Name == "name" && nodeText(cap.Node, ctx.File.Bytes) == "panic" {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "panic!() in production code; return a Result instead",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: rs/todo ---

type rsTodo struct{}

func (rsTodo) ID() string               { return "rs/todo" }
func (rsTodo) Description() string      { return "todo!() macro left in code" }
func (rsTodo) DefaultSeverity() core.Severity { return core.Warning }

func (r rsTodo) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, macroQuery, lang) {
		if cap.Name == "name" && nodeText(cap.Node, ctx.File.Bytes) == "todo" {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "todo!() left in code; implement or track in issue tracker",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: rs/unimplemented ---

type rsUnimplemented struct{}

func (rsUnimplemented) ID() string               { return "rs/unimplemented" }
func (rsUnimplemented) Description() string      { return "unimplemented!() macro" }
func (rsUnimplemented) DefaultSeverity() core.Severity { return core.Warning }

func (r rsUnimplemented) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, macroQuery, lang) {
		if cap.Name == "name" && nodeText(cap.Node, ctx.File.Bytes) == "unimplemented" {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "unimplemented!() will panic at runtime",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: rs/dbg ---

type rsDbg struct{}

func (rsDbg) ID() string               { return "rs/dbg" }
func (rsDbg) Description() string      { return "dbg!() macro left in code" }
func (rsDbg) DefaultSeverity() core.Severity { return core.Warning }

func (r rsDbg) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, macroQuery, lang) {
		if cap.Name == "name" && nodeText(cap.Node, ctx.File.Bytes) == "dbg" {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  "dbg!() debug macro left in code; remove before shipping",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: rs/unsafe-block ---

type rsUnsafeBlock struct{}

const unsafeQuery = `(unsafe_block) @blk`

func (rsUnsafeBlock) ID() string               { return "rs/unsafe-block" }
func (rsUnsafeBlock) Description() string      { return "unsafe block requires manual safety audit" }
func (rsUnsafeBlock) DefaultSeverity() core.Severity { return core.Warning }

func (r rsUnsafeBlock) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, unsafeQuery, lang) {
		ctx.Report(core.Diagnostic{
			RuleID:   r.ID(),
			Severity: r.DefaultSeverity(),
			Message:  "unsafe block; ensure invariants are documented with a SAFETY comment",
			Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
		})
	}
}

// --- rule: rs/allow-attribute ---

type rsAllowAttribute struct{}

const attrQuery = `
(attribute_item
  (attribute
    (identifier) @name)) @attr
`

func (rsAllowAttribute) ID() string               { return "rs/allow-attribute" }
func (rsAllowAttribute) Description() string      { return "#[allow(...)] suppresses compiler warnings" }
func (rsAllowAttribute) DefaultSeverity() core.Severity { return core.Info }

func (r rsAllowAttribute) Check(ctx *core.RunContext) {
	cfg := ctx.RuleConfig(r.ID())
	// configurable list of lint names to flag; default flags unused/dead_code.
	flagged := cfg.Strings("flag")
	if len(flagged) == 0 {
		flagged = []string{"unused", "dead_code", "unused_variables", "unused_imports"}
	}
	flagSet := make(map[string]bool, len(flagged))
	for _, f := range flagged {
		flagSet[f] = true
	}

	seen := map[uint32]bool{}
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, attrQuery, lang) {
		if cap.Name != "attr" || seen[cap.Node.StartByte()] {
			continue
		}
		// Check if the attribute name is "allow".
		attr := cap.Node.NamedChild(0)
		if attr == nil || attr.NamedChildCount() == 0 {
			continue
		}
		nameNode := attr.NamedChild(0)
		if nameNode == nil || nodeText(nameNode, ctx.File.Bytes) != "allow" {
			continue
		}
		// Check the lint arguments.
		if attr.NamedChildCount() < 2 {
			continue
		}
		for i := 1; i < int(attr.NamedChildCount()); i++ {
			lint := strings.TrimSpace(nodeText(attr.NamedChild(i), ctx.File.Bytes))
			if flagSet[lint] {
				seen[cap.Node.StartByte()] = true
				ctx.Report(core.Diagnostic{
					RuleID:   r.ID(),
					Severity: r.DefaultSeverity(),
					Message:  fmt.Sprintf("#[allow(%s)] suppresses a compiler warning; fix the underlying issue", lint),
					Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
				})
				break
			}
		}
	}
}

// --- rule: rs/clone ---

type rsClone struct{}

func (rsClone) ID() string               { return "rs/clone" }
func (rsClone) Description() string      { return ".clone() call; verify it is necessary" }
func (rsClone) DefaultSeverity() core.Severity { return core.Info }

func (r rsClone) Check(ctx *core.RunContext) {
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, methodCallQuery, lang) {
		if cap.Name == "method" && nodeText(cap.Node, ctx.File.Bytes) == "clone" {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  ".clone() may be unnecessary; consider borrowing or using Arc<T>",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

// --- rule: rs/print ---

type rsPrint struct{}

func (rsPrint) ID() string               { return "rs/print" }
func (rsPrint) Description() string      { return "println!/print!/eprintln! macro left in code" }
func (rsPrint) DefaultSeverity() core.Severity { return core.Info }

func (r rsPrint) Check(ctx *core.RunContext) {
	printMacros := map[string]bool{"println": true, "print": true, "eprintln": true, "eprint": true}
	for cap := range tsutil.Query(ctx.Tree, ctx.File.Bytes, macroQuery, lang) {
		if cap.Name == "name" && printMacros[nodeText(cap.Node, ctx.File.Bytes)] {
			ctx.Report(core.Diagnostic{
				RuleID:   r.ID(),
				Severity: r.DefaultSeverity(),
				Message:  nodeText(cap.Node, ctx.File.Bytes) + "!() left in code; use a structured logger",
				Range:    tsutil.NodeRange(cap.Node, ctx.File.Bytes, ctx.File.Path),
			})
		}
	}
}

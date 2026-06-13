// Package rust provides the Rust language analyzer and all built-in rules.
package rust

import (
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
	tree, err := pool.ParseBytes(src)
	if err != nil {
		return nil, fmt.Errorf("rust parse: %w", err)
	}
	return tree, nil
}

func (Analyzer) CommentRanges(_ []byte, tree *sitter.Tree) []core.ByteRange {
	if tree == nil {
		return nil
	}
	return tsutil.CommentRangesFromTree(tree, "line_comment", "block_comment")
}

var (
	lang = sitterrust.GetLanguage()
	pool = tsutil.NewParserPool(lang)

	methodCallQ = tsutil.MustQuery(`
(call_expression
  function: (field_expression
    field: (field_identifier) @method)) @call
`, lang)

	macroQ = tsutil.MustQuery(`(macro_invocation macro: (identifier) @name) @mac`, lang)

	unsafeQ = tsutil.MustQuery(`(unsafe_block) @blk`, lang)

	attrQ = tsutil.MustQuery(`
(attribute_item
  (attribute
    (identifier) @name)) @attr
`, lang)
)

// methodRule flags .method() calls by name.
func methodRule(id, desc string, sev core.Severity, method, msg string) core.Rule {
	return core.NewRule(id, desc, sev, func(r core.Rule, ctx *core.RunContext) {
		for cap := range methodCallQ.Run(ctx.Tree, ctx.File.Bytes) {
			if cap.Name == "method" && tsutil.NodeText(cap.Node, ctx.File.Bytes) == method {
				tsutil.ReportNode(ctx, r, cap.Node, msg)
			}
		}
	})
}

// macroRule flags name!() macro invocations by name.
func macroRule(id, desc string, sev core.Severity, name, msg string) core.Rule {
	return core.NewRule(id, desc, sev, func(r core.Rule, ctx *core.RunContext) {
		for cap := range macroQ.Run(ctx.Tree, ctx.File.Bytes) {
			if cap.Name == "name" && tsutil.NodeText(cap.Node, ctx.File.Bytes) == name {
				tsutil.ReportNode(ctx, r, cap.Node, msg)
			}
		}
	})
}

var printMacros = map[string]bool{"println": true, "print": true, "eprintln": true, "eprint": true}

func (Analyzer) Rules() []core.Rule {
	return []core.Rule{
		tsutil.TrailingCommaRule("rs/trailing-comma",
			"multiline struct literal without trailing comma", lang, "(field_initializer_list) @lit"),

		tsutil.BlankAroundRule("rs/blank-around-items",
			"struct/enum/trait/impl without blank lines around it", lang,
			"[(struct_item) (enum_item) (trait_item) (impl_item)] @item",
			map[string]bool{"source_file": true, "declaration_list": true},
			map[string]string{"struct_item": "struct", "enum_item": "enum", "trait_item": "trait", "impl_item": "impl block"},
			"line_comment", "block_comment", "attribute_item"),

		methodRule("rs/unwrap", ".unwrap() call; propagate errors with ? instead", core.Warning,
			"unwrap", ".unwrap() panics on Err/None; propagate with ? or handle explicitly"),
		methodRule("rs/expect", ".expect() call; propagate errors with ? instead", core.Warning,
			"expect", ".expect() panics on Err/None; propagate with ? or handle explicitly"),
		macroRule("rs/panic", "panic!() macro", core.Warning,
			"panic", "panic!() in production code; return a Result instead"),
		macroRule("rs/todo", "todo!() macro left in code", core.Warning,
			"todo", "todo!() left in code; implement or track in issue tracker"),
		macroRule("rs/unimplemented", "unimplemented!() macro", core.Warning,
			"unimplemented", "unimplemented!() will panic at runtime"),
		macroRule("rs/dbg", "dbg!() macro left in code", core.Warning,
			"dbg", "dbg!() debug macro left in code; remove before shipping"),

		core.NewRule("rs/unsafe-block", "unsafe block requires manual safety audit", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range unsafeQ.Run(ctx.Tree, ctx.File.Bytes) {
					tsutil.ReportNode(ctx, r, cap.Node, "unsafe block; ensure invariants are documented with a SAFETY comment")
				}
			}),

		core.NewRule("rs/allow-attribute", "#[allow(...)] suppresses compiler warnings", core.Info,
			func(r core.Rule, ctx *core.RunContext) {
				flagged := ctx.RuleConfig(r.ID()).Strings("flag")
				if len(flagged) == 0 {
					flagged = []string{"unused", "dead_code", "unused_variables", "unused_imports"}
				}
				flagSet := make(map[string]bool, len(flagged))
				for _, f := range flagged {
					flagSet[f] = true
				}
				src := ctx.File.Bytes
				// One attribute_item can match once per identifier inside it.
				seen := map[uint32]bool{}
				for m := range attrQ.Matches(ctx.Tree, src) {
					if start := m.Node("attr").StartByte(); seen[start] {
						continue
					} else {
						seen[start] = true
					}
					attr := m.Node("attr").NamedChild(0)
					if attr == nil || attr.NamedChildCount() < 2 {
						continue
					}
					if tsutil.NodeText(attr.NamedChild(0), src) != "allow" {
						continue
					}
					for i := 1; i < int(attr.NamedChildCount()); i++ {
						lint := strings.TrimSpace(tsutil.NodeText(attr.NamedChild(i), src))
						if flagSet[lint] {
							tsutil.ReportNode(ctx, r, m.Node("attr"),
								fmt.Sprintf("#[allow(%s)] suppresses a compiler warning; fix the underlying issue", lint))
							break
						}
					}
				}
			}),

		methodRule("rs/clone", ".clone() call; verify it is necessary", core.Info,
			"clone", ".clone() may be unnecessary; consider borrowing or using Arc<T>"),

		core.NewRule("rs/print", "println!/print!/eprintln! macro left in code", core.Info,
			func(r core.Rule, ctx *core.RunContext) {
				for cap := range macroQ.Run(ctx.Tree, ctx.File.Bytes) {
					if cap.Name == "name" && printMacros[tsutil.NodeText(cap.Node, ctx.File.Bytes)] {
						tsutil.ReportNode(ctx, r, cap.Node,
							tsutil.NodeText(cap.Node, ctx.File.Bytes)+"!() left in code; use a structured logger")
					}
				}
			}),
	}
}

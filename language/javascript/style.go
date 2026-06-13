// Layout/whitespace style rules shared by JavaScript, TypeScript, and
// Svelte <script> blocks: one declaration per line, blank lines around
// returns/blocks/functions, and object literal layout.
package javascript

import (
	"fmt"
	"strings"

	"eastwood/core"
	"eastwood/tsutil"

	sitter "github.com/smacker/go-tree-sitter"
)

// EmbeddedScriptRules returns the full shared JS/TS rule set compiled for the
// TypeScript grammar, for analyzers that embed scripts (Svelte).
func EmbeddedScriptRules() []core.Rule {
	rules := append(jsRules(tsLang), styleRules(tsLang)...)
	return append(rules, tsOnlyRules(tsLang)...)
}

// ParseEmbeddedScript parses (masked) script source with the TypeScript
// grammar. The TS grammar is a superset of JS, so plain-JS scripts parse fine.
func ParseEmbeddedScript(src []byte) (*sitter.Tree, error) {
	return tsPool.ParseBytes(src)
}

// --- shared layout helpers ---

func countNewlines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}

// blockParents are the node types inside which blank-line layout applies.
// A statement that is e.g. the sole body of an if has no siblings to
// separate from, so only these containers are checked.
var blockParents = map[string]bool{
	"program":         true,
	"statement_block": true,
	"class_body":      true,
}

// attachedStart walks upward over comments (and TS decorators) that sit
// directly above node with no blank line, returning the top of the group.
// A doc comment belongs to its declaration, so the blank line is required
// above the comment, not between comment and declaration.
func attachedStart(node *sitter.Node) *sitter.Node {
	cur := node
	for {
		prev := cur.PrevNamedSibling()
		if prev == nil {
			return cur
		}
		t := prev.Type()
		if t != "comment" && t != "decorator" {
			return cur
		}
		if cur.StartPoint().Row-prev.EndPoint().Row > 1 {
			return cur
		}
		cur = prev
	}
}

// attachedEnd swallows trailing comments on the same line as node's last line.
func attachedEnd(node *sitter.Node) *sitter.Node {
	cur := node
	for {
		next := cur.NextNamedSibling()
		if next == nil || next.Type() != "comment" {
			return cur
		}
		if next.StartPoint().Row != cur.EndPoint().Row {
			return cur
		}
		cur = next
	}
}

// missingBlankBefore reports whether node needs (and lacks) a blank line
// above it. First statement in its container is exempt.
func missingBlankBefore(node *sitter.Node, src []byte) bool {
	start := attachedStart(node)
	prev := start.PrevNamedSibling()
	if prev == nil {
		return false
	}
	return countNewlines(src[prev.EndByte():start.StartByte()]) < 2
}

// missingBlankAfter reports whether node needs (and lacks) a blank line
// below it. Last statement in its container is exempt.
func missingBlankAfter(node *sitter.Node, src []byte) bool {
	end := attachedEnd(node)
	next := end.NextNamedSibling()
	if next == nil {
		return false
	}
	return countNewlines(src[end.EndByte():next.StartByte()]) < 2
}

func multiline(node *sitter.Node) bool {
	return node.StartPoint().Row != node.EndPoint().Row
}

// declaresFunction reports whether a let/const/var statement binds a
// function or arrow function value.
func declaresFunction(node *sitter.Node) bool {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		c := node.NamedChild(i)
		if c.Type() != "variable_declarator" {
			continue
		}
		v := c.ChildByFieldName("value")
		if v == nil {
			continue
		}
		switch v.Type() {
		case "arrow_function", "function_expression", "function":
			return true
		}
	}
	return false
}

func defKind(t string) string {
	switch t {
	case "class_declaration":
		return "class"
	case "method_definition":
		return "method"
	default:
		return "function"
	}
}

var blockKind = map[string]string{
	"if_statement":     "if block",
	"switch_statement": "switch block",
	"for_statement":    "loop",
	"for_in_statement": "loop",
	"while_statement":  "loop",
	"do_statement":     "loop",
}

// styleRules returns the layout rules compiled for the given grammar.
func styleRules(l *sitter.Language) []core.Rule {
	return append(layoutRules(l), flowLayoutRules(l)...)
}

func layoutRules(l *sitter.Language) []core.Rule {
	var (
		declQ   = tsutil.MustQuery(`[(lexical_declaration) (variable_declaration)] @decl`, l)
		returnQ = tsutil.MustQuery(`(return_statement) @ret`, l)
		blockQ  = tsutil.MustQuery(`
[(if_statement) (switch_statement) (for_statement) (for_in_statement)
 (while_statement) (do_statement)] @block
`, l)
		objectQ = tsutil.MustQuery(`(object) @obj`, l)
		defQ    = tsutil.MustQuery(`
[(function_declaration) (generator_function_declaration) (class_declaration)
 (method_definition) (lexical_declaration) (variable_declaration)] @def
`, l)
	)

	return []core.Rule{
		core.NewRule("js/decl-per-line", "multiple declarations in one statement must be one per line", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				src := ctx.File.Bytes
				for cap := range declQ.Run(ctx.Tree, src) {
					node := cap.Node
					// for (let i = 0, j = n; ...) is idiomatic; skip loop headers.
					if p := node.Parent(); p != nil && (p.Type() == "for_statement" || p.Type() == "for_in_statement") {
						continue
					}
					var declarators []*sitter.Node
					for i := 0; i < int(node.NamedChildCount()); i++ {
						if c := node.NamedChild(i); c.Type() == "variable_declarator" {
							declarators = append(declarators, c)
						}
					}
					if len(declarators) < 2 {
						continue
					}
					// Every declarator on its own line, none sharing the keyword line.
					seen := map[uint32]bool{node.StartPoint().Row: true}
					bad := false
					for _, d := range declarators {
						row := d.StartPoint().Row
						if seen[row] {
							bad = true
							break
						}
						seen[row] = true
					}
					if bad {
						kw := "let"
						if node.ChildCount() > 0 {
							kw = tsutil.NodeText(node.Child(0), src)
						}
						tsutil.ReportNode(ctx, r, node,
							fmt.Sprintf("declare one variable per line: write '%s //' then each declaration on its own line", kw))
					}
				}
			}),

		core.NewRule("js/blank-before-return", "return statement without a blank line before it", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				src := ctx.File.Bytes
				for cap := range returnQ.Run(ctx.Tree, src) {
					node := cap.Node
					p := node.Parent()
					if p == nil || (p.Type() != "statement_block" && p.Type() != "program") {
						continue
					}
					if missingBlankBefore(node, src) {
						tsutil.ReportNode(ctx, r, node, "add a blank line before the return statement")
					}
				}
			}),

		core.NewRule("js/blank-around-blocks", "multi-line loop/conditional without blank lines around it", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				src := ctx.File.Bytes
				for cap := range blockQ.Run(ctx.Tree, src) {
					node := cap.Node
					if !multiline(node) {
						continue
					}
					p := node.Parent()
					if p == nil || !blockParents[p.Type()] {
						continue
					}
					kind := blockKind[node.Type()]
					if missingBlankBefore(node, src) {
						tsutil.ReportNode(ctx, r, node, "add a blank line before this "+kind)
					}
					if missingBlankAfter(node, src) {
						tsutil.ReportNode(ctx, r, node, "add a blank line after this "+kind)
					}
				}
			}),

		core.NewRule("js/object-layout", "multi-key objects one key per line; single-key objects on one line", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				src := ctx.File.Bytes
				for cap := range objectQ.Run(ctx.Tree, src) {
					node := cap.Node
					var props []*sitter.Node
					for i := 0; i < int(node.NamedChildCount()); i++ {
						if c := node.NamedChild(i); c.Type() != "comment" {
							props = append(props, c)
						}
					}
					switch {
					case len(props) == 0:
						continue
					case len(props) == 1:
						// Only flag if the object could actually fit on one line.
						if multiline(node) && !multiline(props[0]) {
							tsutil.ReportNode(ctx, r, node, "object with a single key should be written on one line")
						}
					default:
						seen := map[uint32]bool{node.StartPoint().Row: true}
						bad := false
						for _, p := range props {
							row := p.StartPoint().Row
							if seen[row] {
								bad = true
								break
							}
							seen[row] = true
						}
						if bad {
							tsutil.ReportNode(ctx, r, node,
								fmt.Sprintf("object with %d keys should have each key on its own line", len(props)))
						}
					}
				}
			}),

		core.NewRule("js/blank-around-functions", "function or class without blank lines around it", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				src := ctx.File.Bytes
				for cap := range defQ.Run(ctx.Tree, src) {
					node := cap.Node
					typ := node.Type()
					if typ == "lexical_declaration" || typ == "variable_declaration" {
						// const f = () => {...}: only multi-line arrow/function bindings
						// count as function definitions for layout purposes.
						if !declaresFunction(node) || !multiline(node) {
							continue
						}
					}
					// Blank lines go around the export statement, not inside it.
					outer := node
					if p := node.Parent(); p != nil && p.Type() == "export_statement" {
						outer = p
					}
					p := outer.Parent()
					if p == nil || !blockParents[p.Type()] {
						continue
					}
					kind := defKind(typ)
					if missingBlankBefore(outer, src) {
						tsutil.ReportNode(ctx, r, node, "add a blank line before this "+kind)
					}
					if missingBlankAfter(outer, src) {
						tsutil.ReportNode(ctx, r, node, "add a blank line after this "+kind)
					}
				}
			}),
	}
}

// --- flow layout rules: js/boolean-layout, js/break-per-line, js/control-flow ---

var logicalOps = map[string]bool{"&&": true, "||": true}

func isLogicalExpr(n *sitter.Node, src []byte) bool {
	if n == nil || n.Type() != "binary_expression" {
		return false
	}
	op := n.ChildByFieldName("operator")
	return op != nil && logicalOps[tsutil.NodeText(op, src)]
}

// chainLogicalOps collects the operator tokens of a &&/|| chain, descending
// through directly nested logical operands but not into parentheses — a
// parenthesised group is its own chain, so inline (a || b) mixing is fine.
func chainLogicalOps(n *sitter.Node, src []byte, out []*sitter.Node) []*sitter.Node {
	if !isLogicalExpr(n, src) {
		return out
	}
	out = append(out, n.ChildByFieldName("operator"))
	out = chainLogicalOps(n.ChildByFieldName("left"), src, out)
	out = chainLogicalOps(n.ChildByFieldName("right"), src, out)
	return out
}

// ternaryChainCount counts directly nested ternaries (not crossing parens).
func ternaryChainCount(n *sitter.Node) int {
	if n == nil || n.Type() != "ternary_expression" {
		return 0
	}
	return 1 + ternaryChainCount(n.ChildByFieldName("consequence")) +
		ternaryChainCount(n.ChildByFieldName("alternative"))
}

// startsItsLine reports whether node is the first non-whitespace on its line.
func startsItsLine(n *sitter.Node, src []byte) bool {
	col := int(n.StartPoint().Column)
	start := int(n.StartByte())
	for _, c := range src[start-col : start] {
		if c != ' ' && c != '\t' {
			return false
		}
	}
	return true
}

var controlFlowTypes = `[(if_statement) (for_statement) (for_in_statement)
 (while_statement) (do_statement) (switch_statement) (try_statement)] @cf`

func flowLayoutRules(l *sitter.Language) []core.Rule {
	var (
		logicalQ = tsutil.MustQuery(`(binary_expression operator: ["&&" "||"] @op) @expr`, l)
		ternaryQ = tsutil.MustQuery(`(ternary_expression) @t`, l)
		jumpQ    = tsutil.MustQuery(`[(break_statement) (continue_statement)] @jump`, l)
		cfQ      = tsutil.MustQuery(controlFlowTypes, l)
		ifQ      = tsutil.MustQuery(`(if_statement) @if`, l)
	)

	return []core.Rule{
		core.NewRule("js/boolean-layout", "multi-condition boolean or nested ternary not split across lines", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				src := ctx.File.Bytes
				for cap := range logicalQ.Run(ctx.Tree, src) {
					if cap.Name != "expr" || isLogicalExpr(cap.Node.Parent(), src) {
						continue // operator capture, or not the chain root
					}
					ops := chainLogicalOps(cap.Node, src, nil)
					if len(ops) < 2 {
						continue
					}
					for _, op := range ops {
						if !startsItsLine(op, src) {
							tsutil.ReportNode(ctx, r, cap.Node, fmt.Sprintf(
								"boolean chain with %d conditions; put each condition on its own line starting with its && or ||",
								len(ops)+1))
							break
						}
					}
				}
				for cap := range ternaryQ.Run(ctx.Tree, src) {
					if p := cap.Node.Parent(); p != nil && p.Type() == "ternary_expression" {
						continue // only the outermost ternary of a chain
					}
					if ternaryChainCount(cap.Node) >= 2 &&
						cap.Node.StartPoint().Row == cap.Node.EndPoint().Row {
						tsutil.ReportNode(ctx, r, cap.Node,
							"nested ternary on one line; split it with ? and : starting their own lines")
					}
				}
			}),

		core.NewRule("js/break-per-line", "break/continue sharing a line with other code", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				src := ctx.File.Bytes
				for cap := range jumpQ.Run(ctx.Tree, src) {
					if !startsItsLine(cap.Node, src) {
						kw := strings.TrimSuffix(cap.Node.Type(), "_statement")
						tsutil.ReportNode(ctx, r, cap.Node, kw+" should be on its own line")
					}
				}
			}),

		core.NewRule("js/control-flow", "control flow inline with other code, or if/else without braces", core.Warning,
			func(r core.Rule, ctx *core.RunContext) {
				src := ctx.File.Bytes
				for cap := range cfQ.Run(ctx.Tree, src) {
					// else if is one construct (the if may share the else's line);
					// a labeled statement legitimately precedes its body on the
					// same line, including Svelte's reactive `$: if (x) {`.
					if p := cap.Node.Parent(); p != nil &&
						(p.Type() == "else_clause" || p.Type() == "labeled_statement") {
						continue
					}
					if !startsItsLine(cap.Node, src) {
						tsutil.ReportNode(ctx, r, cap.Node,
							"control-flow statement should start on its own line")
					}
				}
				for cap := range ifQ.Run(ctx.Tree, src) {
					node := cap.Node
					alt := node.ChildByFieldName("alternative")
					inElseChain := node.Parent() != nil && node.Parent().Type() == "else_clause"
					if alt == nil && !inElseChain {
						continue // bare if; braces not mandated
					}
					if cons := node.ChildByFieldName("consequence"); cons != nil && cons.Type() != "statement_block" {
						tsutil.ReportNode(ctx, r, cons,
							"if/else branches require braces; wrap this branch in { }")
					}
					if alt != nil {
						if body := alt.NamedChild(0); body != nil &&
							body.Type() != "statement_block" && body.Type() != "if_statement" {
							tsutil.ReportNode(ctx, r, body,
								"if/else branches require braces; wrap this branch in { }")
						}
					}
				}
			}),
	}
}

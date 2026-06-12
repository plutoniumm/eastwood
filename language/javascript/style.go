// Layout/whitespace style rules shared by JavaScript, TypeScript, and
// Svelte <script> blocks: one declaration per line, blank lines around
// returns/blocks/functions, and object literal layout.
package javascript

import (
	"fmt"

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

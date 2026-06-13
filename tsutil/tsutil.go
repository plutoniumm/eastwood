// Package tsutil provides ergonomic helpers on top of the tree-sitter Go bindings.
package tsutil

import (
	"context"
	"iter"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
	"unsafe"

	"eastwood/core"

	sitter "github.com/smacker/go-tree-sitter"
)

// Capture is a single named capture from a tree-sitter query match.
type Capture struct {
	Node *sitter.Node
	Name string
}

// ── ParserPool ────────────────────────────────────────────────────────────────

// ParserPool is a pool of reusable parsers configured for a single language.
// Allocate once per language with NewParserPool; call ParseBytes per file.
type ParserPool struct {
	pool sync.Pool
}

// NewParserPool returns a pool of parsers configured for lang.
func NewParserPool(lang *sitter.Language) *ParserPool {
	return &ParserPool{pool: sync.Pool{New: func() any {
		p := sitter.NewParser()
		p.SetLanguage(lang)
		return p
	}}}
}

// ParseBytes parses src and returns the syntax tree.
func (pp *ParserPool) ParseBytes(src []byte) (*sitter.Tree, error) {
	p := pp.pool.Get().(*sitter.Parser)
	defer pp.pool.Put(p)
	return p.ParseCtx(context.Background(), nil, src)
}

// ── CompiledQuery ─────────────────────────────────────────────────────────────

type queryCacheKey struct {
	lang uintptr
	q    string
}

var compiledQueries sync.Map

// CompiledQuery is a precompiled, reusable tree-sitter query.
// Create at package initialisation with MustQuery; call Run or Matches per file.
type CompiledQuery struct {
	q     *sitter.Query
	names []string // capture index → name, precomputed (CaptureNameForId is a cgo call)
}

// MustQuery compiles queryStr against lang and returns a CompiledQuery.
// Panics on an invalid query string (programmer error). Results are cached
// globally, so calling MustQuery with the same (queryStr, lang) pair is free
// after the first call.
func MustQuery(queryStr string, lang *sitter.Language) CompiledQuery {
	key := queryCacheKey{lang: uintptr(unsafe.Pointer(lang)), q: queryStr}
	if v, ok := compiledQueries.Load(key); ok {
		return v.(CompiledQuery)
	}
	q, err := sitter.NewQuery([]byte(queryStr), lang)
	if err != nil {
		panic("tsutil.MustQuery: invalid query: " + err.Error() + "\n" + queryStr)
	}
	names := make([]string, q.CaptureCount())
	for i := range names {
		names[i] = q.CaptureNameForId(uint32(i))
	}
	cq := CompiledQuery{q: q, names: names}
	compiledQueries.Store(key, cq)
	return cq
}

// Run executes the query against tree and yields each capture, flattened
// across matches. For queries with multiple captures whose correlation
// matters, use Matches instead.
func (cq CompiledQuery) Run(tree *sitter.Tree, src []byte) iter.Seq[Capture] {
	return func(yield func(Capture) bool) {
		cursor := sitter.NewQueryCursor()
		cursor.Exec(cq.q, tree.RootNode())
		for {
			m, ok := cursor.NextMatch()
			if !ok {
				return
			}
			for _, c := range m.Captures {
				if !yield(Capture{Node: c.Node, Name: cq.names[c.Index]}) {
					return
				}
			}
		}
	}
}

// Match is one query match with its captures still correlated.
type Match struct {
	names []string
	caps  []sitter.QueryCapture
}

// Node returns the capture with the given name, or nil if the match has none.
func (m Match) Node(name string) *sitter.Node {
	for _, c := range m.caps {
		if m.names[c.Index] == name {
			return c.Node
		}
	}
	return nil
}

// Matches executes the query against tree and yields one Match per query
// match, so multi-capture queries keep their captures correlated (Run
// flattens them, which loses which @a belongs to which @b).
func (cq CompiledQuery) Matches(tree *sitter.Tree, src []byte) iter.Seq[Match] {
	return func(yield func(Match) bool) {
		cursor := sitter.NewQueryCursor()
		cursor.Exec(cq.q, tree.RootNode())
		for {
			m, ok := cursor.NextMatch()
			if !ok {
				return
			}
			if !yield(Match{names: cq.names, caps: m.Captures}) {
				return
			}
		}
	}
}

// Query is a convenience wrapper for one-off queries. Prefer declaring a
// package-level CompiledQuery via MustQuery for any query that runs per-file.
func Query(tree *sitter.Tree, src []byte, queryStr string, lang *sitter.Language) iter.Seq[Capture] {
	return MustQuery(queryStr, lang).Run(tree, src)
}

// ── Position / range helpers ──────────────────────────────────────────────────

// ReportNode emits a diagnostic for rule r spanning node, at the rule's
// default severity. This is the standard one-liner for AST rules.
func ReportNode(ctx *core.RunContext, r core.Rule, node *sitter.Node, msg string) {
	ReportNodeSev(ctx, r, r.DefaultSeverity(), node, msg)
}

// ReportNodeSev is ReportNode with an explicit severity.
func ReportNodeSev(ctx *core.RunContext, r core.Rule, sev core.Severity, node *sitter.Node, msg string) {
	ctx.Report(core.Diagnostic{
		RuleID:   r.ID(),
		Severity: sev,
		Message:  msg,
		Range:    NodeRange(node, ctx.File.Bytes, ctx.File.Path),
	})
}

// NodeRange converts a tree-sitter node's span into a core.Range.
func NodeRange(node *sitter.Node, src []byte, filePath string) core.Range {
	return core.Range{
		Start: pointToPos(node.StartPoint(), node.StartByte(), src, filePath),
		End:   pointToPos(node.EndPoint(), node.EndByte(), src, filePath),
	}
}

func pointToPos(p sitter.Point, byteOffset uint32, src []byte, filePath string) core.Position {
	row := int(p.Row)
	byteCol := int(p.Column)
	offset := int(byteOffset)
	rowStart := offset - byteCol
	runeCol := utf8.RuneCount(src[rowStart:rowStart+byteCol]) + 1
	return core.Position{
		File:   filePath,
		Line:   row + 1,
		Col:    runeCol,
		Offset: offset,
	}
}

// ── Node helpers ──────────────────────────────────────────────────────────────

// NodeText returns the source text covered by node.
func NodeText(node *sitter.Node, src []byte) string {
	return string(src[node.StartByte():node.EndByte()])
}

// HasAncestor reports whether any ancestor of node has one of the given types.
func HasAncestor(node *sitter.Node, types ...string) bool {
	for cur := node.Parent(); cur != nil; cur = cur.Parent() {
		if slices.Contains(types, cur.Type()) {
			return true
		}
	}
	return false
}

// ── Comment helpers ───────────────────────────────────────────────────────────

// CommentRangesFromTree extracts byte ranges of all comment nodes from the
// tree in a single walk.
func CommentRangesFromTree(tree *sitter.Tree, commentNodeTypes ...string) []core.ByteRange {
	var ranges []core.ByteRange
	collectComments(tree.RootNode(), commentNodeTypes, &ranges)
	return ranges
}

func collectComments(node *sitter.Node, types []string, out *[]core.ByteRange) {
	if slices.Contains(types, node.Type()) {
		*out = append(*out, core.ByteRange{
			Start: int(node.StartByte()),
			End:   int(node.EndByte()),
		})
		return
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		collectComments(node.Child(i), types, out)
	}
}

// InComment reports whether offset falls within any of the provided comment ranges.
func InComment(offset int, ranges []core.ByteRange) bool {
	for _, r := range ranges {
		if r.Contains(offset) {
			return true
		}
	}
	return false
}

// WalkNodes calls fn for every node in the subtree rooted at node, pre-order.
// Returning false from fn stops the walk.
func WalkNodes(node *sitter.Node, fn func(*sitter.Node) bool) {
	if !fn(node) {
		return
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		WalkNodes(node.Child(i), fn)
	}
}

// TodoCommentRule flags TODO-style markers in comments. kinds are matched in
// order (case-insensitive, substring); msgSuffix completes the message
// "<KIND> comment<msgSuffix>". commentTypes are the grammar's comment node
// types (e.g. "comment", or "line_comment"+"block_comment" for Rust).
func TodoCommentRule(id string, lang *sitter.Language, kinds []string, msgSuffix string, commentTypes ...string) core.Rule {
	queryStr := "(" + commentTypes[0] + ") @c"
	if len(commentTypes) > 1 {
		queryStr = "[(" + strings.Join(commentTypes, ") (") + ")] @c"
	}
	q := MustQuery(queryStr, lang)
	return core.NewRule(id, "TODO/FIXME/HACK comment left in code", core.Info,
		func(r core.Rule, ctx *core.RunContext) {
			for cap := range q.Run(ctx.Tree, ctx.File.Bytes) {
				text := strings.ToUpper(NodeText(cap.Node, ctx.File.Bytes))
				for _, kind := range kinds {
					if strings.Contains(text, kind) {
						ReportNode(ctx, r, cap.Node, kind+" comment"+msgSuffix)
						break
					}
				}
			}
		})
}

// TrailingCommaRule flags multiline literals — objects, dicts, composite
// literals, whatever nodeQuery selects — whose last element is not followed
// by a trailing comma before the closing delimiter.
func TrailingCommaRule(id, desc string, lang *sitter.Language, nodeQuery string) core.Rule {
	q := MustQuery(nodeQuery, lang)
	return core.NewRule(id, desc, core.Warning,
		func(r core.Rule, ctx *core.RunContext) {
			src := ctx.File.Bytes
			for cap := range q.Run(ctx.Tree, src) {
				n := cap.Node
				if n.StartPoint().Row == n.EndPoint().Row {
					continue
				}
				last := n.NamedChild(int(n.NamedChildCount()) - 1)
				for last != nil && (last.Type() == "comment" || last.Type() == "line_comment" || last.Type() == "block_comment") {
					last = last.PrevNamedSibling()
				}
				if last == nil {
					continue
				}
				rest := strings.TrimSpace(string(src[last.EndByte() : n.EndByte()-1]))
				if !strings.HasPrefix(rest, ",") {
					ReportNode(ctx, r, last, "add a trailing comma after the last element of this multiline literal")
				}
			}
		})
}

// --- blank-line layout helpers ---

func countNewlines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}

// attachedStart walks upward over attach-type siblings (comments, decorators,
// attribute_item, ...) that sit directly above node with no blank line: a doc
// comment or #[derive] belongs to its declaration, so the blank line is
// required above the group, not inside it.
func attachedStart(node *sitter.Node, attach []string) *sitter.Node {
	cur := node
	for {
		prev := cur.PrevNamedSibling()
		if prev == nil || !slices.Contains(attach, prev.Type()) ||
			cur.StartPoint().Row-prev.EndPoint().Row > 1 {
			return cur
		}
		cur = prev
	}
}

// attachedEnd swallows trailing attach-type siblings on the node's last line.
func attachedEnd(node *sitter.Node, attach []string) *sitter.Node {
	cur := node
	for {
		next := cur.NextNamedSibling()
		if next == nil || !slices.Contains(attach, next.Type()) ||
			next.StartPoint().Row != cur.EndPoint().Row {
			return cur
		}
		cur = next
	}
}

// BlankAroundRule flags nodes selected by nodeQuery that lack a blank line
// above or below them. containers limits the check to nodes whose parent is a
// listed type (first/last child are exempt via sibling checks); attach lists
// node types that group with the declaration (doc comments, decorators,
// attributes). An export_statement wrapper is hoisted automatically.
func BlankAroundRule(id, desc string, lang *sitter.Language, nodeQuery string,
	containers map[string]bool, kinds map[string]string, attach ...string) core.Rule {
	q := MustQuery(nodeQuery, lang)
	return core.NewRule(id, desc, core.Warning,
		func(r core.Rule, ctx *core.RunContext) {
			src := ctx.File.Bytes
			for cap := range q.Run(ctx.Tree, src) {
				n := cap.Node
				kind := kinds[n.Type()]
				if p := n.Parent(); p != nil && p.Type() == "export_statement" {
					n = p
				}
				p := n.Parent()
				if p == nil || !containers[p.Type()] {
					continue
				}
				start := attachedStart(n, attach)
				if prev := start.PrevNamedSibling(); prev != nil &&
					countNewlines(src[prev.EndByte():start.StartByte()]) < 2 {
					ReportNode(ctx, r, n, "add a blank line before this "+kind)
				}
				end := attachedEnd(n, attach)
				if next := end.NextNamedSibling(); next != nil &&
					countNewlines(src[end.EndByte():next.StartByte()]) < 2 {
					ReportNode(ctx, r, n, "add a blank line after this "+kind)
				}
			}
		})
}

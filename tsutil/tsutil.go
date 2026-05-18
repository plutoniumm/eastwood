// Package tsutil provides ergonomic helpers on top of the tree-sitter Go bindings.
package tsutil

import (
	"context"
	"iter"
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
// Create at package initialisation with MustQuery; call Run per file.
type CompiledQuery struct {
	q *sitter.Query
}

// MustQuery compiles queryStr against lang and returns a CompiledQuery.
// Panics on an invalid query string (programmer error). Results are cached
// globally, so calling MustQuery with the same (queryStr, lang) pair is free
// after the first call.
func MustQuery(queryStr string, lang *sitter.Language) CompiledQuery {
	key := queryCacheKey{lang: uintptr(unsafe.Pointer(lang)), q: queryStr}
	if v, ok := compiledQueries.Load(key); ok {
		return CompiledQuery{q: v.(*sitter.Query)}
	}
	q, err := sitter.NewQuery([]byte(queryStr), lang)
	if err != nil {
		panic("tsutil.MustQuery: invalid query: " + err.Error() + "\n" + queryStr)
	}
	compiledQueries.Store(key, q)
	return CompiledQuery{q: q}
}

// Run executes the query against tree and yields each capture.
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
				name := cq.q.CaptureNameForId(c.Index)
				if !yield(Capture{Node: c.Node, Name: name}) {
					return
				}
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

// ── Comment helpers ───────────────────────────────────────────────────────────

// CommentRangesFromTree extracts byte ranges of all comment nodes from the tree.
func CommentRangesFromTree(tree *sitter.Tree, commentNodeTypes ...string) []core.ByteRange {
	var ranges []core.ByteRange
	for _, t := range commentNodeTypes {
		collectComments(tree.RootNode(), t, &ranges)
	}
	return ranges
}

func collectComments(node *sitter.Node, typeName string, out *[]core.ByteRange) {
	if node.Type() == typeName {
		*out = append(*out, core.ByteRange{
			Start: int(node.StartByte()),
			End:   int(node.EndByte()),
		})
		return
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		collectComments(node.Child(i), typeName, out)
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

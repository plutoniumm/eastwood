// Package tsutil provides ergonomic helpers on top of the tree-sitter Go bindings.
package tsutil

import (
	"iter"
	"unicode/utf8"

	"eastwood/core"

	sitter "github.com/smacker/go-tree-sitter"
)

// Capture is a single named capture from a tree-sitter query match.
type Capture struct {
	Node *sitter.Node
	Name string
}

// Query executes a tree-sitter query against the given tree and yields each
// capture as an iter.Seq. Panics on invalid query strings (programmer error).
func Query(tree *sitter.Tree, src []byte, queryStr string, lang *sitter.Language) iter.Seq[Capture] {
	q, err := sitter.NewQuery([]byte(queryStr), lang)
	if err != nil {
		panic("tsutil.Query: invalid query: " + err.Error() + "\n" + queryStr)
	}
	return func(yield func(Capture) bool) {
		cursor := sitter.NewQueryCursor()
		cursor.Exec(q, tree.RootNode())
		for {
			m, ok := cursor.NextMatch()
			if !ok {
				return
			}
			for _, c := range m.Captures {
				name := q.CaptureNameForId(c.Index)
				if !yield(Capture{Node: c.Node, Name: name}) {
					return
				}
			}
		}
	}
}

// NodeRange converts a tree-sitter node's span into a core.Range.
func NodeRange(node *sitter.Node, src []byte, filePath string) core.Range {
	return core.Range{
		Start: pointToPos(node.StartPoint(), node.StartByte(), src, filePath),
		End:   pointToPos(node.EndPoint(), node.EndByte(), src, filePath),
	}
}

// pointToPos converts a tree-sitter Point (0-indexed row, byte column) plus
// byte offset into a core.Position with 1-indexed line and rune column.
func pointToPos(p sitter.Point, byteOffset uint32, src []byte, filePath string) core.Position {
	row := int(p.Row)
	byteCol := int(p.Column)
	offset := int(byteOffset)

	// Find the start of this row in src to compute rune column.
	rowStart := offset - byteCol
	runeCol := utf8.RuneCount(src[rowStart:rowStart+byteCol]) + 1

	return core.Position{
		File:   filePath,
		Line:   row + 1,
		Col:    runeCol,
		Offset: offset,
	}
}

// CommentRangesFromTree extracts byte ranges of all comment nodes from the tree.
// The commentNodeType should be "comment" for Python and LaTeX tree-sitter grammars.
func CommentRangesFromTree(tree *sitter.Tree, commentNodeType string) []core.ByteRange {
	var ranges []core.ByteRange
	collectComments(tree.RootNode(), commentNodeType, &ranges)
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

// InComment reports whether the given byte offset falls within any of the
// provided comment ranges.
func InComment(offset int, ranges []core.ByteRange) bool {
	for _, r := range ranges {
		if r.Contains(offset) {
			return true
		}
	}
	return false
}

// WalkNodes calls fn for every node in the subtree rooted at node, in
// pre-order. Returning false from fn stops the walk.
func WalkNodes(node *sitter.Node, fn func(*sitter.Node) bool) {
	if !fn(node) {
		return
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		WalkNodes(node.Child(i), fn)
	}
}

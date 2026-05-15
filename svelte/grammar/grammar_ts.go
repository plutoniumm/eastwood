//go:build ts_svelte

// Package grammar provides the tree-sitter-svelte language binding (CGo build).
// Requires src/parser.c (and optionally src/scanner.c) from
// https://github.com/Himujjal/tree-sitter-svelte to be present in this directory.
// Run 'make setup-svelte' to download them automatically.
package grammar

/*
#cgo CFLAGS: -std=c11 -fvisibility=hidden
#include "src/parser.c"
#include "src/scanner.c"
*/
import "C"
import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// GetLanguage returns the compiled tree-sitter-svelte language.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_svelte()))
}

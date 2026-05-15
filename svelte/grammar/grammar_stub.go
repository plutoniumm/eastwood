//go:build !ts_svelte

// Package grammar provides the tree-sitter-svelte language binding.
// In stub mode (default build), GetLanguage returns nil and the Svelte
// analyzer falls back to text/regex rules only.
//
// To enable full tree-sitter parsing for Svelte:
//
//  1. Run: make setup-svelte          (downloads C grammar sources)
//  2. Build with: go build -tags ts_svelte ./...
package grammar

import sitter "github.com/smacker/go-tree-sitter"

// GetLanguage returns nil in stub mode.
func GetLanguage() *sitter.Language { return nil }

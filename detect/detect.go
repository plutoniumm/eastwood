// Package detect infers the language of a source file from its extension or content.
package detect

import (
	"bytes"
	"path/filepath"
	"strings"
)

var extMap = map[string]string{
	".py":     "python",
	".pyi":    "python",
	".tex":    "latex",
	".cls":    "latex",
	".sty":    "latex",
	".bib":    "latex",
	".go":     "go",
	".rs":     "rust",
	".js":     "javascript",
	".jsx":    "javascript",
	".mjs":    "javascript",
	".cjs":    "javascript",
	".ts":     "typescript",
	".tsx":    "typescript",
	".mts":    "typescript",
	".cts":    "typescript",
	".svelte": "svelte",
}

// FromPath returns the language for the given file path based on extension.
func FromPath(path string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	lang, ok := extMap[ext]
	return lang, ok
}

// FromContent attempts to infer the language from file content.
// Returns ("", false) when no confident match is found.
func FromContent(src []byte) (lang string, confident bool) {
	head := src
	if len(head) > 4096 {
		head = head[:4096]
	}

	scores := map[string]int{
		"python":     scorePython(head),
		"latex":      scoreLatex(head),
		"go":         scoreGo(head),
		"rust":       scoreRust(head),
		"javascript": scoreJS(head),
		"svelte":     scoreSvelte(head),
	}

	best, bestScore := "", 0
	second := 0
	for lang, score := range scores {
		if score > bestScore {
			second = bestScore
			bestScore = score
			best = lang
		} else if score > second {
			second = score
		}
	}

	const threshold = 2
	if bestScore < threshold {
		return "", false
	}
	return best, bestScore >= 4 && bestScore > second+1
}

func scorePython(src []byte) int {
	score := 0
	for _, m := range [][]byte{
		[]byte("import "), []byte("from "), []byte("def "), []byte("class "),
		[]byte("if __name__"), []byte("#!/usr/bin/env python"), []byte("# -*- coding:"),
	} {
		if bytes.Contains(src, m) {
			score++
		}
	}
	return score
}

func scoreLatex(src []byte) int {
	score := 0
	for _, m := range [][]byte{
		[]byte(`\documentclass`), []byte(`\begin{document}`), []byte(`\usepackage`),
		[]byte(`\section`), []byte(`\begin{`), []byte(`\end{`), []byte(`\cite{`),
	} {
		if bytes.Contains(src, m) {
			score++
		}
	}
	return score
}

func scoreGo(src []byte) int {
	score := 0
	for _, m := range [][]byte{
		[]byte("package "), []byte("import ("), []byte("func "),
		[]byte(":= "), []byte("go func"), []byte("interface{"), []byte("struct {"),
	} {
		if bytes.Contains(src, m) {
			score++
		}
	}
	return score
}

func scoreRust(src []byte) int {
	score := 0
	for _, m := range [][]byte{
		[]byte("fn "), []byte("let "), []byte("use "), []byte("pub "),
		[]byte("impl "), []byte("struct "), []byte("enum "), []byte("match "),
	} {
		if bytes.Contains(src, m) {
			score++
		}
	}
	return score
}

func scoreJS(src []byte) int {
	score := 0
	for _, m := range [][]byte{
		[]byte("function "), []byte("const "), []byte("let "), []byte("var "),
		[]byte("=>"), []byte("require("), []byte("import "), []byte("export "),
	} {
		if bytes.Contains(src, m) {
			score++
		}
	}
	return score
}

func scoreSvelte(src []byte) int {
	score := 0
	for _, m := range [][]byte{
		[]byte("<script"), []byte("</script>"), []byte("<style"),
		[]byte("{#if"), []byte("{#each"), []byte("{@html"), []byte("$:"),
	} {
		if bytes.Contains(src, m) {
			score++
		}
	}
	return score
}

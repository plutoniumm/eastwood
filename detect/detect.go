// Package detect infers the language of a source file from its extension or content.
package detect

import (
	"bytes"
	"path/filepath"
	"strings"
)

// extMap maps file extensions (including leading dot) to language names.
var extMap = map[string]string{
	".py":  "python",
	".pyi": "python",
	".tex": "latex",
	".cls": "latex",
	".sty": "latex",
	".bib": "latex",
}

// FromPath returns the language for the given file path based on extension.
// Returns ("", false) when the extension is not recognised.
func FromPath(path string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	lang, ok := extMap[ext]
	return lang, ok
}

// FromContent attempts to infer the language from the file content.
// Returns ("", false) when no confident match is found.
func FromContent(src []byte) (lang string, confident bool) {
	// Limit scan to first 4 KB for speed.
	head := src
	if len(head) > 4096 {
		head = head[:4096]
	}

	pyScore := scorePython(head)
	texScore := scoreLatex(head)

	const threshold = 2
	if pyScore >= threshold && pyScore > texScore {
		return "python", pyScore >= 4
	}
	if texScore >= threshold && texScore > pyScore {
		return "latex", texScore >= 4
	}
	return "", false
}

func scorePython(src []byte) int {
	score := 0
	markers := [][]byte{
		[]byte("import "),
		[]byte("from "),
		[]byte("def "),
		[]byte("class "),
		[]byte("if __name__"),
		[]byte("#!/usr/bin/env python"),
		[]byte("#!/usr/bin/python"),
		[]byte("# -*- coding:"),
	}
	for _, m := range markers {
		if bytes.Contains(src, m) {
			score++
		}
	}
	return score
}

func scoreLatex(src []byte) int {
	score := 0
	markers := [][]byte{
		[]byte(`\documentclass`),
		[]byte(`\begin{document}`),
		[]byte(`\usepackage`),
		[]byte(`\section`),
		[]byte(`\begin{`),
		[]byte(`\end{`),
		[]byte(`\cite{`),
		[]byte(`%`), // LaTeX comments are very common
	}
	for _, m := range markers {
		if bytes.Contains(src, m) {
			score++
		}
	}
	return score
}

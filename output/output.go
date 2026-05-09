// Package output formats and writes diagnostics to stdout.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"eastwood/core"
)

// Format selects the output format.
type Format int

const (
	FormatText Format = iota
	FormatJSON
)

// ParseFormat parses "text" or "json"; defaults to text on unknown values.
func ParseFormat(s string) Format {
	if s == "json" {
		return FormatJSON
	}
	return FormatText
}

// Formatter writes diagnostics to an output stream.
type Formatter interface {
	// WriteDiagnostics emits all diagnostics for one file. Called once per file.
	WriteDiagnostics(diags []core.Diagnostic)
	// WriteError emits a non-diagnostic error (e.g. parse failure).
	WriteError(file, msg string)
}

// New returns a Formatter for the given format. Color is enabled when f is
// text and stdout is an interactive terminal.
func New(f Format, w io.Writer) Formatter {
	if f == FormatJSON {
		return &jsonFormatter{w: w}
	}
	return &textFormatter{w: w, color: isTerminal(w)}
}

// --- text formatter ---

type textFormatter struct {
	w     io.Writer
	color bool
}

var severityColor = map[core.Severity]string{
	core.Info:    "\033[36m",    // cyan
	core.Warning: "\033[33m",    // yellow
	core.Error:   "\033[31;1m",  // bold red
}

const resetColor = "\033[0m"
const dimColor = "\033[2m"

func (f *textFormatter) WriteDiagnostics(diags []core.Diagnostic) {
	for _, d := range diags {
		f.writeDiag(d)
	}
}

func (f *textFormatter) writeDiag(d core.Diagnostic) {
	loc := fmt.Sprintf("%s:%d:%d", d.Range.Start.File, d.Range.Start.Line, d.Range.Start.Col)
	sev := d.Severity.String()
	ruleID := "[" + d.RuleID + "]"

	if f.color {
		col, ok := severityColor[d.Severity]
		if !ok {
			col = ""
		}
		sev = col + sev + resetColor
		ruleID = dimColor + ruleID + resetColor
	}

	fmt.Fprintf(f.w, "%s: %s: %s %s\n", loc, sev, d.Message, ruleID)
}

func (f *textFormatter) WriteError(file, msg string) {
	if f.color {
		fmt.Fprintf(f.w, "%s: \033[31;1merror\033[0m: %s\n", file, msg)
	} else {
		fmt.Fprintf(f.w, "%s: error: %s\n", file, msg)
	}
}

// --- JSON (NDJSON) formatter ---

type jsonFormatter struct {
	w io.Writer
}

type jsonDiag struct {
	RuleID   string `json:"rule_id"`
	Severity string `json:"severity"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Message  string `json:"message"`
}

func (f *jsonFormatter) WriteDiagnostics(diags []core.Diagnostic) {
	for _, d := range diags {
		obj := jsonDiag{
			RuleID:   d.RuleID,
			Severity: d.Severity.String(),
			File:     d.Range.Start.File,
			Line:     d.Range.Start.Line,
			Col:      d.Range.Start.Col,
			Message:  d.Message,
		}
		b, _ := json.Marshal(obj)
		f.w.Write(b)
		f.w.Write([]byte{'\n'})
	}
}

func (f *jsonFormatter) WriteError(file, msg string) {
	obj := jsonDiag{
		RuleID:   "le/internal-error",
		Severity: "error",
		File:     file,
		Message:  msg,
	}
	b, _ := json.Marshal(obj)
	f.w.Write(b)
	f.w.Write([]byte{'\n'})
}

// isTerminal reports whether w is an interactive terminal (Unix only).
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

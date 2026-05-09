package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"eastwood/config"
	"eastwood/core"
	"eastwood/detect"
	"eastwood/latex"
	"eastwood/output"
	"eastwood/python"
	"eastwood/runner"
)

const usage = `le — lint eastwood

Usage:
  le [flags] [path ...]
  cat file.tex | le --lang tex

  Paths can be files or directories. With no path, the current directory is
  linted. When reading from stdin, --lang is required (or le will try to
  guess the language from content).

Flags:
`

var allAnalyzers = []core.Analyzer{
	python.Analyzer{},
	latex.Analyzer{},
}

func main() {
	fs := flag.NewFlagSet("le", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr)
	}

	langFlag := fs.String("lang", "", "force language (python, latex); required when reading stdin")
	formatFlag := fs.String("format", "text", "output format: text, json")
	jobsFlag := fs.Int("jobs", 0, "parallel workers (default: number of CPUs)")
	configFlag := fs.String("config", "", "path to eastwood.toml (default: walk up from CWD)")
	rulesFlag := fs.String("rule", "", "restrict run to a single rule ID (e.g. py/bare-except)")
	listFlag := fs.Bool("list-rules", false, "list all available rules and exit")
	versionFlag := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	if *versionFlag {
		fmt.Println("le dev")
		os.Exit(0)
	}

	if *listFlag {
		listRules()
		os.Exit(0)
	}

	analyzers := allAnalyzers
	if *rulesFlag != "" {
		analyzers = filterToRule(*rulesFlag, analyzers)
		if len(analyzers) == 0 {
			fmt.Fprintf(os.Stderr, "le: unknown rule %q\n", *rulesFlag)
			os.Exit(2)
		}
	}

	// Detect stdin.
	stdinStat, _ := os.Stdin.Stat()
	fromStdin := (stdinStat.Mode() & os.ModeCharDevice) == 0

	// Resolve config.
	var cfg *config.Config
	var cfgErr error
	if *configFlag != "" {
		// Load a single specified config file.
		abs, err := filepath.Abs(*configFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "le: %v\n", err)
			os.Exit(2)
		}
		cfg, cfgErr = config.Chain(filepath.Dir(abs))
	} else {
		startDir, _ := os.Getwd()
		cfg, cfgErr = config.Chain(startDir)
	}
	if cfgErr != nil {
		fmt.Fprintf(os.Stderr, "le: %v\n", cfgErr)
		os.Exit(2)
	}

	jobs := *jobsFlag
	if jobs <= 0 {
		jobs = runtime.NumCPU()
	}

	opts := runner.Options{
		Paths:   fs.Args(),
		Format:  output.ParseFormat(*formatFlag),
		Jobs:    jobs,
		Writer:  os.Stdout,
	}

	if fromStdin {
		lang := *langFlag
		if lang == "" {
			// Try to detect from content — read a chunk.
			preview := make([]byte, 4096)
			n, _ := os.Stdin.Read(preview)
			preview = preview[:n]
			guessed, confident := detect.FromContent(preview)
			if guessed == "" {
				fmt.Fprintln(os.Stderr, "le: cannot detect language from stdin; use --lang")
				os.Exit(2)
			}
			lang = guessed
			if !confident {
				fmt.Fprintf(os.Stderr, "le: guessing language %q from content; use --lang to be explicit\n", lang)
			}
			// Prepend the already-read bytes to stdin.
			opts.Stdin = prependReader(preview, os.Stdin)
		} else {
			opts.Stdin = os.Stdin
		}
		opts.StdinLang = lang
	}

	ctx := context.Background()
	code := runner.Run(ctx, analyzers, cfg, opts)
	os.Exit(code)
}

// listRules prints all available rules to stdout.
func listRules() {
	fmt.Printf("%-40s %-10s %s\n", "RULE ID", "SEVERITY", "DESCRIPTION")
	fmt.Println(strings.Repeat("-", 80))
	for _, an := range allAnalyzers {
		for _, rule := range an.Rules() {
			fmt.Printf("%-40s %-10s %s\n",
				rule.ID(),
				rule.DefaultSeverity().String(),
				rule.Description())
		}
	}
}

// filterToRule returns analyzers containing only the specified rule.
func filterToRule(ruleID string, analyzers []core.Analyzer) []core.Analyzer {
	type singleRule struct {
		core.Analyzer
		rule core.Rule
	}
	for _, an := range analyzers {
		for _, rule := range an.Rules() {
			if rule.ID() == ruleID {
				return []core.Analyzer{singleAnalyzer{Analyzer: an, rules: []core.Rule{rule}}}
			}
		}
	}
	return nil
}

// singleAnalyzer wraps an Analyzer but exposes only a subset of rules.
type singleAnalyzer struct {
	core.Analyzer
	rules []core.Rule
}

func (s singleAnalyzer) Rules() []core.Rule { return s.rules }

// prependReader returns a reader that first yields prefix, then reads from r.
func prependReader(prefix []byte, r *os.File) *multiReader {
	return &multiReader{prefix: prefix, r: r}
}

type multiReader struct {
	prefix []byte
	r      *os.File
}

func (m *multiReader) Read(p []byte) (int, error) {
	if len(m.prefix) > 0 {
		n := copy(p, m.prefix)
		m.prefix = m.prefix[n:]
		return n, nil
	}
	return m.r.Read(p)
}

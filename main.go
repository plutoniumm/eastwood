package main

import (
	"context"
	goflag "flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	"eastwood/core"
	"eastwood/language/golang"
	"eastwood/language/javascript"
	"eastwood/language/latex"
	"eastwood/language/python"
	"eastwood/language/rust"
	"eastwood/language/svelte"
	"eastwood/runner"
)

const usageText = `eastwood — a fast, pluggable linter

Usage:
  eastwood [flags] [path ...]
  cat file.tex | eastwood --lang tex

Paths can be files or directories. With no path, the current directory is
linted. When reading from stdin, --lang is required (or eastwood guesses
from content and warns).

Flags:
`

var version = "dev"

var allAnalyzers = []core.Analyzer{
	python.Analyzer{},
	latex.Analyzer{},
	golang.Analyzer{},
	rust.Analyzer{},
	javascript.JSAnalyzer{},
	javascript.TSAnalyzer{},
	svelte.Analyzer{},
}

func main() {
	fs := goflag.NewFlagSet("eastwood", goflag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, usageText)
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr)
	}

	langFlag := fs.String("lang", "", "force language (python, latex, go, rust, javascript, typescript, svelte)")
	formatFlag := fs.String("format", "text", "output format: text, json")
	jobsFlag := fs.Int("jobs", 0, "parallel workers (default: CPU count)")
	configFlag := fs.String("config", "", "path to eastwood.toml (default: walk up from CWD)")
	ruleFlag := fs.String("rule", "", "restrict to one rule ID (e.g. go/panic)")
	listFlag := fs.Bool("list-rules", false, "list all available rules and exit")
	noCache := fs.Bool("no-cache", false, "disable the on-disk result cache")
	versionFlag := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	if *versionFlag {
		fmt.Println("eastwood " + version)
		os.Exit(0)
	}

	analyzers := allAnalyzers
	if *ruleFlag != "" {
		analyzers = filterToRule(*ruleFlag, analyzers)
		if len(analyzers) == 0 {
			fmt.Fprintf(os.Stderr, "eastwood: unknown rule %q\n", *ruleFlag)
			os.Exit(2)
		}
	}

	if *listFlag {
		listRules(analyzers)
		os.Exit(0)
	}

	// Config discovery.
	startDir, _ := os.Getwd()
	if *configFlag != "" {
		startDir = *configFlag
	}
	cfg, err := runner.LoadConfig(startDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "eastwood: %v\n", err)
		os.Exit(2)
	}

	// Cache. The key includes the config fingerprint so editing any
	// eastwood.toml in the chain invalidates cached results.
	var c *runner.Cache
	if !*noCache {
		// The version is part of the key so every release invalidates the
		// cache — rule-logic changes don't alter rule IDs or config, so without
		// this a new build would serve stale diagnostics.
		c, err = runner.NewCache(append(runner.RuleIDs(analyzers),
			"config:"+cfg.Fingerprint, "version:"+version))
		if err != nil {
			fmt.Fprintf(os.Stderr, "eastwood: cache init: %v (continuing without cache)\n", err)
		}
	}

	jobs := *jobsFlag
	if jobs <= 0 {
		jobs = runtime.NumCPU()
	}

	opts := runner.Options{
		Paths:  fs.Args(),
		Format: runner.ParseFormat(*formatFlag),
		Jobs:   jobs,
		Writer: os.Stdout,
		Cache:  c,
	}

	// Detect stdin.
	stdinStat, _ := os.Stdin.Stat()
	if (stdinStat.Mode() & os.ModeCharDevice) == 0 {
		lang := *langFlag
		if lang == "" {
			preview := make([]byte, 4096)
			n, _ := os.Stdin.Read(preview)
			preview = preview[:n]
			guessed, confident := runner.DetectLanguage(preview)
			if guessed == "" {
				fmt.Fprintln(os.Stderr, "eastwood: cannot detect language from stdin; use --lang")
				os.Exit(2)
			}
			lang = guessed
			if !confident {
				fmt.Fprintf(os.Stderr, "eastwood: guessing language %q from content; pass --lang to be explicit\n", lang)
			}
			opts.Stdin = prependBytes(preview, os.Stdin)
		} else {
			opts.Stdin = os.Stdin
		}
		opts.StdinLang = lang
	}

	code := runner.Run(context.Background(), analyzers, cfg, opts)
	os.Exit(code)
}

func listRules(analyzers []core.Analyzer) {
	fmt.Printf("%-42s %-10s %s\n", "RULE ID", "SEVERITY", "DESCRIPTION")
	fmt.Println(strings.Repeat("-", 90))
	seen := make(map[string]bool)
	for _, an := range analyzers {
		for _, rule := range an.Rules() {
			if seen[rule.ID()] {
				continue
			}
			seen[rule.ID()] = true
			fmt.Printf("%-42s %-10s %s\n", rule.ID(), rule.DefaultSeverity(), rule.Description())
		}
	}
}

func filterToRule(ruleID string, analyzers []core.Analyzer) []core.Analyzer {
	for _, an := range analyzers {
		for _, rule := range an.Rules() {
			if rule.ID() == ruleID {
				return []core.Analyzer{singleAnalyzer{Analyzer: an, rules: []core.Rule{rule}}}
			}
		}
	}
	return nil
}

type singleAnalyzer struct {
	core.Analyzer
	rules []core.Rule
}

func (s singleAnalyzer) Rules() []core.Rule { return s.rules }

// prependBytes returns a reader that first replays prefix, then reads from r.
type prependReader struct {
	prefix []byte
	r      *os.File
}

func prependBytes(prefix []byte, r *os.File) *prependReader {
	return &prependReader{prefix: prefix, r: r}
}

func (p *prependReader) Read(buf []byte) (int, error) {
	if len(p.prefix) > 0 {
		n := copy(buf, p.prefix)
		p.prefix = p.prefix[n:]
		return n, nil
	}
	return p.r.Read(buf)
}

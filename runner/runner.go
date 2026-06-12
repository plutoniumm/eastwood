// Package runner orchestrates file discovery, parallel analysis, caching, and output.
package runner

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"eastwood/core"
)

// Options configure a single run.
type Options struct {
	Paths     []string  // files or directories; empty = CWD
	Stdin     io.Reader // non-nil → read from stdin (requires StdinLang)
	StdinLang string
	Format    Format
	Jobs      int       // 0 = runtime.NumCPU()
	Writer    io.Writer // defaults to os.Stdout
	Cache     *Cache
}

// compiledAnalyzer is an analyzer with its rule set and language config
// resolved once per run instead of once per file.
type compiledAnalyzer struct {
	an      core.Analyzer
	rules   []core.Rule // enabled rules only
	langCfg ResolvedLang
}

func compileAnalyzers(analyzers []core.Analyzer, cfg *Config) (byLang, byExt map[string]*compiledAnalyzer) {
	byLang = make(map[string]*compiledAnalyzer, len(analyzers))
	byExt = make(map[string]*compiledAnalyzer)
	for _, an := range analyzers {
		langCfg := cfg.LangConfig(an.Language())
		ca := &compiledAnalyzer{an: an, langCfg: langCfg}
		for _, rule := range an.Rules() {
			if langCfg.IsEnabled(rule.ID()) {
				ca.rules = append(ca.rules, rule)
			}
		}
		byLang[an.Language()] = ca
		for _, ext := range an.Extensions() {
			byExt[strings.ToLower(ext)] = ca
		}
	}
	return byLang, byExt
}

// Run executes the linter and returns an exit code: 0 clean, 1 findings, 2 error.
func Run(ctx context.Context, analyzers []core.Analyzer, cfg *Config, opts Options) (exitCode int) {
	if opts.Writer == nil {
		opts.Writer = os.Stdout
	}
	if opts.Jobs <= 0 {
		opts.Jobs = runtime.NumCPU()
	}

	buf := bufio.NewWriterSize(opts.Writer, 256*1024)
	defer buf.Flush()

	formatter := newFormatter(opts.Format, buf)
	byLang, byExt := compileAnalyzers(analyzers, cfg)

	if opts.Stdin != nil {
		data, err := io.ReadAll(opts.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "eastwood: reading stdin: %v\n", err)
			return 2
		}
		ca := byLang[opts.StdinLang]
		if ca == nil {
			fmt.Fprintf(os.Stderr, "eastwood: no analyzer for language %q\n", opts.StdinLang)
			return 2
		}
		diags, errMsg := processBytes("<stdin>", data, ca, opts.Cache)
		if errMsg != "" {
			formatter.WriteError("<stdin>", errMsg)
			return 2
		}
		formatter.WriteDiagnostics(diags)
		if hasFinding(diags, cfg.FailOn) {
			return 1
		}
		return 0
	}

	paths := opts.Paths
	if len(paths) == 0 {
		paths = []string{"."}
	}
	type workItem struct {
		path string
		ca   *compiledAnalyzer
	}
	var queue []workItem
	enqueue := func(path string) {
		ext := strings.ToLower(filepath.Ext(path))
		if ca := byExt[ext]; ca != nil {
			queue = append(queue, workItem{path: path, ca: ca})
		}
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "eastwood: %v\n", err)
			return 2
		}
		if !info.IsDir() {
			enqueue(p)
			continue
		}
		if err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
					return filepath.SkipDir
				}
				return nil
			}
			enqueue(path)
			return nil
		}); err != nil {
			fmt.Fprintf(os.Stderr, "eastwood: %v\n", err)
			return 2
		}
	}

	if len(queue) == 0 {
		return 0
	}

	work := make(chan workItem, len(queue))
	for _, item := range queue {
		work <- item
	}
	close(work)

	type result struct {
		path   string
		diags  []core.Diagnostic
		errMsg string
	}
	results := make(chan result, opts.Jobs*2)

	var wg sync.WaitGroup
	for range opts.Jobs {
		wg.Go(func() {
			for item := range work {
				data, err := os.ReadFile(item.path)
				if err != nil {
					results <- result{path: item.path, errMsg: err.Error()}
					continue
				}
				diags, errMsg := processBytes(item.path, data, item.ca, opts.Cache)
				results <- result{path: item.path, diags: diags, errMsg: errMsg}
			}
		})
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect all results, then sort by path for deterministic output.
	var allResults []result
	for res := range results {
		allResults = append(allResults, res)
	}
	sort.Slice(allResults, func(i, j int) bool {
		return allResults[i].path < allResults[j].path
	})

	maxSev := core.Severity(-1)
	for _, res := range allResults {
		if res.errMsg != "" {
			formatter.WriteError(res.path, res.errMsg)
			continue
		}
		formatter.WriteDiagnostics(res.diags)
		for _, d := range res.diags {
			if d.Severity > maxSev {
				maxSev = d.Severity
			}
		}
	}

	if maxSev >= cfg.FailOn {
		return 1
	}
	return 0
}

// processBytes parses and lints a single file's contents. It consults the cache
// on entry and writes results back on a miss.
func processBytes(path string, data []byte, ca *compiledAnalyzer, c *Cache) ([]core.Diagnostic, string) {
	// Cache lookup.
	if c != nil {
		if cached, ok := c.Get(data); ok {
			// Re-stamp the file path (cache stores it but let's be sure).
			for i := range cached {
				cached[i].Range.Start.File = path
				cached[i].Range.End.File = path
			}
			return cached, ""
		}
	}

	tree, err := ca.an.Parse(data, path)
	if err != nil {
		return nil, fmt.Sprintf("parse error: %v", err)
	}

	sf := &core.SourceFile{Path: path, Language: ca.an.Language(), Bytes: data}
	commentRanges := ca.an.CommentRanges(data, tree)
	directives := parseDirectives(data, commentRanges)

	var diags []core.Diagnostic

	ctx := &core.RunContext{
		File:          sf,
		Tree:          tree,
		RuleConfigs:   ca.langCfg.Rules,
		CommentRanges: commentRanges,
		Report: func(d core.Diagnostic) {
			if directives.suppresses(d.RuleID, d.Range.Start.Line) {
				return
			}
			diags = append(diags, d)
		},
	}

	for _, rule := range ca.rules {
		rule.Check(ctx)
	}

	sort.Slice(diags, func(i, j int) bool {
		a, b := diags[i].Range.Start, diags[j].Range.Start
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Col != b.Col {
			return a.Col < b.Col
		}
		return diags[i].RuleID < diags[j].RuleID
	})

	// Write to cache on miss.
	if c != nil {
		c.Put(data, diags)
	}

	return diags, ""
}

func hasFinding(diags []core.Diagnostic, threshold core.Severity) bool {
	for _, d := range diags {
		if d.Severity >= threshold {
			return true
		}
	}
	return false
}

// --- inline directive parsing ---

type directiveSet struct {
	fileWide map[string]bool
	lines    map[int][]string
}

var directiveRe = regexp.MustCompile(`eastwood:\s*(disable|disable-next|disable-file)=([^\s]+)`)

func parseDirectives(src []byte, commentRanges []core.ByteRange) directiveSet {
	ds := directiveSet{
		fileWide: make(map[string]bool),
		lines:    make(map[int][]string),
	}
	if len(commentRanges) == 0 {
		return ds
	}
	lineStarts := []int{0}
	for i, b := range src {
		if b == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}
	byteToLine := func(offset int) int {
		line := sort.Search(len(lineStarts), func(i int) bool { return lineStarts[i] > offset }) - 1
		return max(line, 0) + 1
	}
	for _, r := range commentRanges {
		m := directiveRe.FindSubmatch(src[r.Start:r.End])
		if m == nil {
			continue
		}
		kind := string(m[1])
		dirLine := byteToLine(r.Start)
		for ruleID := range strings.SplitSeq(string(m[2]), ",") {
			ruleID = strings.TrimSpace(ruleID)
			if ruleID == "" {
				continue
			}
			switch kind {
			case "disable":
				ds.lines[dirLine] = append(ds.lines[dirLine], ruleID)
			case "disable-next":
				ds.lines[dirLine+1] = append(ds.lines[dirLine+1], ruleID)
			case "disable-file":
				ds.fileWide[ruleID] = true
			}
		}
	}
	return ds
}

func (ds directiveSet) suppresses(ruleID string, line int) bool {
	if ds.fileWide[ruleID] || ds.fileWide["*"] {
		return true
	}
	for _, id := range ds.lines[line] {
		if id == ruleID || id == "*" {
			return true
		}
	}
	return false
}

// Package runner orchestrates file discovery, parallel analysis, caching, and output.
package runner

import (
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

	"eastwood/cache"
	"eastwood/config"
	"eastwood/core"
	"eastwood/output"
)

// Options configure a single run.
type Options struct {
	Paths     []string     // files or directories; empty = CWD
	Stdin     io.Reader    // non-nil → read from stdin (requires StdinLang)
	StdinLang string
	Format    output.Format
	Jobs      int       // 0 = runtime.NumCPU()
	Writer    io.Writer // defaults to os.Stdout
	Cache     *cache.Cache
}

// Run executes the linter and returns an exit code: 0 clean, 1 findings, 2 error.
func Run(ctx context.Context, analyzers []core.Analyzer, cfg *config.Config, opts Options) int {
	if opts.Writer == nil {
		opts.Writer = os.Stdout
	}
	if opts.Jobs <= 0 {
		opts.Jobs = runtime.NumCPU()
	}

	formatter := output.New(opts.Format, opts.Writer)
	extSet := buildExtSet(analyzers)

	// Collect file paths (not content — workers load files themselves).
	type fileSpec struct {
		path string
		lang string
	}

	var specs []fileSpec

	if opts.Stdin != nil {
		data, err := io.ReadAll(opts.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "eastwood: reading stdin: %v\n", err)
			return 2
		}
		specs = append(specs, fileSpec{path: "<stdin>", lang: opts.StdinLang})
		// For stdin we bypass the path queue and process directly.
		an := analyzerFor(opts.StdinLang, analyzers)
		if an == nil {
			fmt.Fprintf(os.Stderr, "eastwood: no analyzer for language %q\n", opts.StdinLang)
			return 2
		}
		diags, errMsg := processBytes("<stdin>", data, an, cfg, opts.Cache)
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
	type pathLang struct{ path, lang string }
	var queue []pathLang
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "eastwood: %v\n", err)
			return 2
		}
		if info.IsDir() {
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
				ext := strings.ToLower(filepath.Ext(path))
				if !extSet[ext] {
					return nil
				}
				lang := extToLang[ext]
				queue = append(queue, pathLang{path: path, lang: lang})
				return nil
			}); err != nil {
				fmt.Fprintf(os.Stderr, "eastwood: %v\n", err)
				return 2
			}
		} else {
			ext := strings.ToLower(filepath.Ext(p))
			lang := extToLang[ext]
			queue = append(queue, pathLang{path: p, lang: lang})
		}
	}

	if len(queue) == 0 {
		return 0
	}

	work := make(chan pathLang, len(queue))
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
	for i := 0; i < opts.Jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range work {
				an := analyzerFor(item.lang, analyzers)
				if an == nil {
					continue
				}
				data, err := os.ReadFile(item.path)
				if err != nil {
					results <- result{path: item.path, errMsg: err.Error()}
					continue
				}
				diags, errMsg := processBytes(item.path, data, an, cfg, opts.Cache)
				results <- result{path: item.path, diags: diags, errMsg: errMsg}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	maxSev := core.Severity(-1)
	for res := range results {
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
func processBytes(path string, data []byte, an core.Analyzer, cfg *config.Config, c *cache.Cache) ([]core.Diagnostic, string) {
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

	tree, err := an.Parse(data, path)
	if err != nil {
		return nil, fmt.Sprintf("parse error: %v", err)
	}

	sf := &core.SourceFile{Path: path, Language: an.Language(), Bytes: data}
	commentRanges := an.CommentRanges(data, tree)
	directives := parseDirectives(data, commentRanges)
	langCfg := cfg.LangConfig(an.Language())

	ruleConfigs := make(map[string]core.RuleConfig, len(langCfg.Rules))
	for id, rc := range langCfg.Rules {
		ruleConfigs[id] = rc
	}

	var mu sync.Mutex
	var diags []core.Diagnostic

	ctx := &core.RunContext{
		File:          sf,
		Tree:          tree,
		RuleConfigs:   ruleConfigs,
		CommentRanges: commentRanges,
		Report: func(d core.Diagnostic) {
			if inAnyComment(d.Range.Start.Offset, commentRanges) {
				return
			}
			if directives.suppresses(d.RuleID, d.Range.Start.Line) {
				return
			}
			mu.Lock()
			diags = append(diags, d)
			mu.Unlock()
		},
	}

	for _, rule := range an.Rules() {
		if !langCfg.IsEnabled(rule.ID()) {
			continue
		}
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

func inAnyComment(offset int, ranges []core.ByteRange) bool {
	for _, r := range ranges {
		if r.Contains(offset) {
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
	lineStarts := []int{0}
	for i, b := range src {
		if b == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}
	byteToLine := func(offset int) int {
		line := sort.Search(len(lineStarts), func(i int) bool { return lineStarts[i] > offset }) - 1
		if line < 0 {
			line = 0
		}
		return line + 1
	}
	for _, r := range commentRanges {
		m := directiveRe.FindSubmatch(src[r.Start:r.End])
		if m == nil {
			continue
		}
		kind := string(m[1])
		dirLine := byteToLine(r.Start)
		for _, ruleID := range strings.Split(string(m[2]), ",") {
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

// --- helpers ---

var extToLang = map[string]string{
	".py": "python", ".pyi": "python",
	".tex": "latex", ".cls": "latex", ".sty": "latex", ".bib": "latex",
	".go":     "go",
	".rs":     "rust",
	".js":     "javascript", ".jsx": "javascript", ".mjs": "javascript", ".cjs": "javascript",
	".ts":     "typescript", ".tsx": "typescript", ".mts": "typescript", ".cts": "typescript",
	".svelte": "svelte",
}

func buildExtSet(analyzers []core.Analyzer) map[string]bool {
	m := make(map[string]bool)
	for _, an := range analyzers {
		for _, ext := range an.Extensions() {
			m[strings.ToLower(ext)] = true
		}
	}
	return m
}

func analyzerFor(lang string, analyzers []core.Analyzer) core.Analyzer {
	for _, an := range analyzers {
		if an.Language() == lang {
			return an
		}
	}
	return nil
}

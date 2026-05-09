// Package runner orchestrates file discovery, parallel analysis, and output.
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

	"eastwood/config"
	"eastwood/core"
	"eastwood/output"
)

// Options configure a single run.
type Options struct {
	Paths    []string       // files or directories to lint; empty = CWD
	Stdin    io.Reader      // non-nil means read from stdin (requires StdinLang)
	StdinLang string        // language for stdin input
	Format   output.Format
	Jobs     int            // 0 = runtime.NumCPU()
	Writer   io.Writer      // destination for output; defaults to os.Stdout
}

// Run executes the linter and returns an exit code (0, 1, or 2).
func Run(ctx context.Context, analyzers []core.Analyzer, cfg *config.Config, opts Options) int {
	if opts.Writer == nil {
		opts.Writer = os.Stdout
	}
	if opts.Jobs <= 0 {
		opts.Jobs = runtime.NumCPU()
	}

	formatter := output.New(opts.Format, opts.Writer)
	extToAnalyzer := buildExtMap(analyzers)

	var files []*core.SourceFile

	if opts.Stdin != nil {
		data, err := io.ReadAll(opts.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "le: reading stdin: %v\n", err)
			return 2
		}
		files = append(files, &core.SourceFile{
			Path:     "<stdin>",
			Language: opts.StdinLang,
			Bytes:    data,
		})
	} else {
		paths := opts.Paths
		if len(paths) == 0 {
			paths = []string{"."}
		}
		var err error
		files, err = discoverFiles(paths, extToAnalyzer)
		if err != nil {
			fmt.Fprintf(os.Stderr, "le: discovering files: %v\n", err)
			return 2
		}
	}

	if len(files) == 0 {
		return 0
	}

	// Work queue — closed once all files are enqueued.
	work := make(chan *core.SourceFile, len(files))
	for _, f := range files {
		work <- f
	}
	close(work)

	// Results are sent per-file; we stream them to output as they arrive.
	type result struct {
		file  string
		diags []core.Diagnostic
		errMsg string
	}
	results := make(chan result, opts.Jobs*2)

	var wg sync.WaitGroup
	for i := 0; i < opts.Jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sf := range work {
				an := analyzerFor(sf.Language, analyzers)
				if an == nil {
					continue
				}
				diags, errMsg := processFile(sf, an, cfg)
				results <- result{file: sf.Path, diags: diags, errMsg: errMsg}
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
			formatter.WriteError(res.file, res.errMsg)
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

// processFile parses one file, runs all enabled rules, filters diagnostics,
// and returns them sorted by position. errMsg is non-empty on parse failure.
func processFile(sf *core.SourceFile, an core.Analyzer, cfg *config.Config) ([]core.Diagnostic, string) {
	tree, err := an.Parse(sf.Bytes)
	if err != nil {
		return nil, fmt.Sprintf("parse error: %v", err)
	}

	commentRanges := an.CommentRanges(sf.Bytes, tree)
	directives := parseDirectives(sf.Bytes, commentRanges)
	langCfg := langConfig(sf.Language, cfg)

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
			// Suppress if in a comment range.
			if inAnyComment(d.Range.Start.Offset, commentRanges) {
				return
			}
			// Apply inline directive suppression.
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

	return diags, ""
}

func inAnyComment(offset int, ranges []core.ByteRange) bool {
	for _, r := range ranges {
		if r.Contains(offset) {
			return true
		}
	}
	return false
}

// --- inline directives ---

// directiveSet holds suppression rules parsed from inline comments.
type directiveSet struct {
	fileWide map[string]bool      // ruleID -> suppress everywhere
	lines    map[int][]string     // line number (1-indexed) -> suppressed rule IDs
}

var directiveRe = regexp.MustCompile(`eastwood:\s*(disable|disable-next|disable-file)=([^\s]+)`)

// parseDirectives scans src for inline eastwood directives embedded in comments.
// commentRanges indicates where comments live so we only look inside them.
func parseDirectives(src []byte, commentRanges []core.ByteRange) directiveSet {
	ds := directiveSet{
		fileWide: make(map[string]bool),
		lines:    make(map[int][]string),
	}

	// Build a fast line-start index.
	lineStarts := []int{0}
	for i, b := range src {
		if b == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}
	byteToLine := func(offset int) int {
		line := sort.Search(len(lineStarts), func(i int) bool {
			return lineStarts[i] > offset
		}) - 1
		if line < 0 {
			line = 0
		}
		return line + 1 // 1-indexed
	}

	for _, r := range commentRanges {
		segment := src[r.Start:r.End]
		m := directiveRe.FindSubmatch(segment)
		if m == nil {
			continue
		}
		kind := string(m[1])
		ruleList := strings.Split(string(m[2]), ",")
		directiveLine := byteToLine(r.Start)

		for _, ruleID := range ruleList {
			ruleID = strings.TrimSpace(ruleID)
			if ruleID == "" {
				continue
			}
			switch kind {
			case "disable":
				ds.lines[directiveLine] = append(ds.lines[directiveLine], ruleID)
			case "disable-next":
				ds.lines[directiveLine+1] = append(ds.lines[directiveLine+1], ruleID)
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

// --- file discovery ---

func discoverFiles(paths []string, extMap map[string]bool) ([]*core.SourceFile, error) {
	var files []*core.SourceFile
	seen := make(map[string]bool)

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if info.IsDir() {
			err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
						return filepath.SkipDir
					}
					return nil
				}
				if !extMap[strings.ToLower(filepath.Ext(path))] {
					return nil
				}
				if seen[path] {
					return nil
				}
				seen[path] = true
				sf, err := loadFile(path)
				if err != nil {
					return err
				}
				files = append(files, sf)
				return nil
			})
			if err != nil {
				return nil, err
			}
		} else {
			if seen[p] {
				continue
			}
			seen[p] = true
			sf, err := loadFile(p)
			if err != nil {
				return nil, err
			}
			files = append(files, sf)
		}
	}
	return files, nil
}

func loadFile(path string) (*core.SourceFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(path))
	lang := extToLang[ext]
	return &core.SourceFile{Path: path, Language: lang, Bytes: data}, nil
}

// extToLang maps known extensions to language names.
var extToLang = map[string]string{
	".py":  "python",
	".pyi": "python",
	".tex": "latex",
	".cls": "latex",
	".sty": "latex",
	".bib": "latex",
}

// buildExtMap returns a set of all extensions handled by the given analyzers.
func buildExtMap(analyzers []core.Analyzer) map[string]bool {
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

func langConfig(lang string, cfg *config.Config) config.ResolvedLang {
	switch lang {
	case "python":
		return cfg.Python
	case "latex":
		return cfg.Latex
	default:
		return config.ResolvedLang{}
	}
}

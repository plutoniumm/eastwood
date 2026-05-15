// Package config loads and merges eastwood.toml configuration files.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"eastwood/core"

	"github.com/BurntSushi/toml"
)

// File is the decoded representation of a single eastwood.toml.
type File struct {
	Linter     linterSection   `toml:"linter"`
	Python     languageSection `toml:"python"`
	Latex      languageSection `toml:"latex"`
	Go         languageSection `toml:"go"`
	Rust       languageSection `toml:"rust"`
	Javascript languageSection `toml:"javascript"`
	Typescript languageSection `toml:"typescript"`
	Svelte     languageSection `toml:"svelte"`
}

type linterSection struct {
	FailOn string `toml:"fail_on"`
}

type languageSection struct {
	Enable  []string                  `toml:"enable"`
	Disable []string                  `toml:"disable"`
	Rules   map[string]map[string]any `toml:"rules"`
}

// Config is the resolved, merged configuration used at runtime.
type Config struct {
	FailOn     core.Severity
	Python     ResolvedLang
	Latex      ResolvedLang
	Go         ResolvedLang
	Rust       ResolvedLang
	Javascript ResolvedLang
	Typescript ResolvedLang
	Svelte     ResolvedLang
}

type ResolvedLang struct {
	Enable  []string
	Disable []string
	Rules   map[string]core.RuleConfig
}

// Chain discovers and loads all eastwood.toml files from startDir up to the
// filesystem root, merges them outermost-first (child overrides parent).
// Returns an error if no config file is found anywhere.
func Chain(startDir string) (*Config, error) {
	paths, err := findChain(startDir)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return merge(nil), nil
	}

	var files []File
	for _, p := range paths {
		f, err := loadFile(p)
		if err != nil {
			return nil, fmt.Errorf("loading %s: %w", p, err)
		}
		files = append(files, f)
	}
	return merge(files), nil
}

func findChain(dir string) ([]string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	var found []string
	prev := ""
	for dir != prev {
		if _, err := os.Stat(filepath.Join(dir, "eastwood.toml")); err == nil {
			found = append(found, filepath.Join(dir, "eastwood.toml"))
		}
		prev = dir
		dir = filepath.Dir(dir)
	}
	// Reverse: outermost first so child overrides parent.
	for i, j := 0, len(found)-1; i < j; i, j = i+1, j-1 {
		found[i], found[j] = found[j], found[i]
	}
	return found, nil
}

func loadFile(path string) (File, error) {
	var f File
	if _, err := toml.DecodeFile(path, &f); err != nil {
		return File{}, err
	}
	return f, nil
}

func merge(files []File) *Config {
	cfg := &Config{
		FailOn:     core.Warning,
		Python:     ResolvedLang{Rules: make(map[string]core.RuleConfig)},
		Latex:      ResolvedLang{Rules: make(map[string]core.RuleConfig)},
		Go:         ResolvedLang{Rules: make(map[string]core.RuleConfig)},
		Rust:       ResolvedLang{Rules: make(map[string]core.RuleConfig)},
		Javascript: ResolvedLang{Rules: make(map[string]core.RuleConfig)},
		Typescript: ResolvedLang{Rules: make(map[string]core.RuleConfig)},
		Svelte:     ResolvedLang{Rules: make(map[string]core.RuleConfig)},
	}
	for _, f := range files {
		if f.Linter.FailOn != "" {
			if sv, err := core.ParseSeverity(f.Linter.FailOn); err == nil {
				cfg.FailOn = sv
			}
		}
		applyLang(&cfg.Python, f.Python)
		applyLang(&cfg.Latex, f.Latex)
		applyLang(&cfg.Go, f.Go)
		applyLang(&cfg.Rust, f.Rust)
		applyLang(&cfg.Javascript, f.Javascript)
		applyLang(&cfg.Typescript, f.Typescript)
		applyLang(&cfg.Svelte, f.Svelte)
	}
	return cfg
}

func applyLang(dst *ResolvedLang, src languageSection) {
	if len(src.Enable) > 0 {
		dst.Enable = src.Enable
	}
	if len(src.Disable) > 0 {
		dst.Disable = src.Disable
	}
	for id, raw := range src.Rules {
		dst.Rules[id] = core.RuleConfig(raw)
	}
}

// LangConfig returns the ResolvedLang for the given language name.
func (c *Config) LangConfig(lang string) ResolvedLang {
	switch lang {
	case "python":
		return c.Python
	case "latex":
		return c.Latex
	case "go":
		return c.Go
	case "rust":
		return c.Rust
	case "javascript":
		return c.Javascript
	case "typescript":
		return c.Typescript
	case "svelte":
		return c.Svelte
	default:
		return ResolvedLang{Rules: make(map[string]core.RuleConfig)}
	}
}

// IsEnabled reports whether ruleID is enabled according to this lang config.
// enable/disable lists support simple glob patterns (e.g. "py/*").
func (rl ResolvedLang) IsEnabled(ruleID string) bool {
	enabled := true
	if len(rl.Enable) > 0 {
		enabled = false
		for _, pat := range rl.Enable {
			if matchGlob(pat, ruleID) {
				enabled = true
				break
			}
		}
	}
	for _, pat := range rl.Disable {
		if matchGlob(pat, ruleID) {
			return false
		}
	}
	return enabled
}

func matchGlob(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	star := -1
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '*' {
			star = i
			break
		}
	}
	if star < 0 {
		return pattern == s
	}
	prefix, suffix := pattern[:star], pattern[star+1:]
	return len(s) >= len(prefix)+len(suffix) &&
		s[:len(prefix)] == prefix &&
		s[len(s)-len(suffix):] == suffix
}

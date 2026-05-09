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
	Linter linterSection            `toml:"linter"`
	Python languageSection          `toml:"python"`
	Latex  languageSection          `toml:"latex"`
}

type linterSection struct {
	FailOn string `toml:"fail_on"` // "info" | "warning" | "error"
}

type languageSection struct {
	Enable  []string                      `toml:"enable"`
	Disable []string                      `toml:"disable"`
	Rules   map[string]map[string]any     `toml:"rules"`
}

// Config is the resolved, merged configuration used at runtime.
type Config struct {
	FailOn      core.Severity
	Python      ResolvedLang
	Latex       ResolvedLang
}

type ResolvedLang struct {
	Enable  []string
	Disable []string
	Rules   map[string]core.RuleConfig // rule ID -> config
}

// Chain discovers and loads all eastwood.toml files from startDir up to the
// filesystem root, then merges them from outermost to innermost (child wins).
// Returns an error if no config file is found anywhere in the chain.
func Chain(startDir string) (*Config, error) {
	paths, err := findChain(startDir)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no eastwood.toml found in %s or any parent directory", startDir)
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

// findChain walks up from dir collecting eastwood.toml paths, returning
// them outermost-first (root → startDir).
func findChain(dir string) ([]string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	var found []string
	prev := ""
	for dir != prev {
		candidate := filepath.Join(dir, "eastwood.toml")
		if _, err := os.Stat(candidate); err == nil {
			found = append(found, candidate)
		}
		prev = dir
		dir = filepath.Dir(dir)
	}

	// Reverse so we get outermost first (parent wins as base, child overrides).
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

// merge applies files in order; later entries (children) override earlier ones.
func merge(files []File) *Config {
	cfg := &Config{
		FailOn: core.Warning,
		Python: ResolvedLang{Rules: make(map[string]core.RuleConfig)},
		Latex:  ResolvedLang{Rules: make(map[string]core.RuleConfig)},
	}

	for _, f := range files {
		if f.Linter.FailOn != "" {
			if sv, err := core.ParseSeverity(f.Linter.FailOn); err == nil {
				cfg.FailOn = sv
			}
		}
		applyLang(&cfg.Python, f.Python)
		applyLang(&cfg.Latex, f.Latex)
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
	for id, rawCfg := range src.Rules {
		dst.Rules[id] = core.RuleConfig(rawCfg)
	}
}

// IsEnabled reports whether the rule with the given ID is enabled according to
// the resolved language config. enable/disable lists support glob patterns
// (e.g. "py/*").
func (rl ResolvedLang) IsEnabled(ruleID string) bool {
	// Default: enabled unless explicitly disabled.
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
			enabled = false
			break
		}
	}
	return enabled
}

// matchGlob handles simple glob patterns with a single '*' wildcard.
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
	prefix := pattern[:star]
	suffix := pattern[star+1:]
	return len(s) >= len(prefix)+len(suffix) &&
		s[:len(prefix)] == prefix &&
		s[len(s)-len(suffix):] == suffix
}

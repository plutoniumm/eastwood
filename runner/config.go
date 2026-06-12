// Config loading: discovers and merges eastwood.toml files.

package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"eastwood/core"

	"github.com/BurntSushi/toml"
)

// File is the decoded representation of a single eastwood.toml. Every
// top-level section other than [linter] is treated as a language section, so
// adding a language to the linter requires no config changes.
type File struct {
	Linter linterSection
	Langs  map[string]languageSection
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
	FailOn core.Severity
	Langs  map[string]ResolvedLang

	// Fingerprint identifies the exact config chain contents, so caches can
	// be invalidated when any eastwood.toml in the chain changes.
	Fingerprint string
}

type ResolvedLang struct {
	Enable  []string
	Disable []string
	Rules   map[string]core.RuleConfig
}

// LoadConfig discovers and loads all eastwood.toml files from startDir up to the
// filesystem root, merges them outermost-first (child overrides parent).
func LoadConfig(startDir string) (*Config, error) {
	paths, err := findChain(startDir)
	if err != nil {
		return nil, err
	}

	h := sha256.New()
	var files []File
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("loading %s: %w", p, err)
		}
		fmt.Fprintf(h, "%s\x00%d\x00", p, len(raw))
		h.Write(raw)
		f, err := parseFile(raw)
		if err != nil {
			return nil, fmt.Errorf("loading %s: %w", p, err)
		}
		files = append(files, f)
	}

	cfg := merge(files)
	cfg.Fingerprint = hex.EncodeToString(h.Sum(nil))
	return cfg, nil
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

func parseFile(raw []byte) (File, error) {
	var sections map[string]toml.Primitive
	md, err := toml.Decode(string(raw), &sections)
	if err != nil {
		return File{}, err
	}
	f := File{Langs: make(map[string]languageSection)}
	for name, prim := range sections {
		if name == "linter" {
			if err := md.PrimitiveDecode(prim, &f.Linter); err != nil {
				return File{}, fmt.Errorf("[linter]: %w", err)
			}
			continue
		}
		var sec languageSection
		if err := md.PrimitiveDecode(prim, &sec); err != nil {
			return File{}, fmt.Errorf("[%s]: %w", name, err)
		}
		f.Langs[name] = sec
	}
	return f, nil
}

func merge(files []File) *Config {
	cfg := &Config{
		FailOn: core.Warning,
		Langs:  make(map[string]ResolvedLang),
	}
	for _, f := range files {
		if f.Linter.FailOn != "" {
			if sv, err := core.ParseSeverity(f.Linter.FailOn); err == nil {
				cfg.FailOn = sv
			}
		}
		for name, sec := range f.Langs {
			dst := cfg.Langs[name]
			if dst.Rules == nil {
				dst.Rules = make(map[string]core.RuleConfig)
			}
			applyLang(&dst, sec)
			cfg.Langs[name] = dst
		}
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
	if rl, ok := c.Langs[lang]; ok {
		return rl
	}
	return ResolvedLang{Rules: make(map[string]core.RuleConfig)}
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

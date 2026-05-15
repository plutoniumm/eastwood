// Package cache provides a SHA256-keyed disk cache for linter results.
// On startup, if the set of active rules has changed since the last run, the
// entire results directory is wiped so stale diagnostics are never served.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"eastwood/core"
)

const schemaVersion = "eastwood-cache-v1"

// Cache is a disk-backed store for per-file diagnostic results.
type Cache struct {
	resultsDir  string
	rulesetHash string
}

// cachedEntry is the on-disk JSON format.
type cachedEntry struct {
	Diagnostics []wireDiagnostic `json:"diagnostics"`
}

// wireDiagnostic is a CGo-free, JSON-serialisable representation of core.Diagnostic.
type wireDiagnostic struct {
	RuleID   string `json:"rule_id"`
	Severity int    `json:"severity"`
	Message  string `json:"message"`
	File     string `json:"file"`
	SLine    int    `json:"start_line"`
	SCol     int    `json:"start_col"`
	SOffset  int    `json:"start_offset"`
	ELine    int    `json:"end_line"`
	ECol     int    `json:"end_col"`
	EOffset  int    `json:"end_offset"`
}

func toWire(d core.Diagnostic) wireDiagnostic {
	return wireDiagnostic{
		RuleID:  d.RuleID,
		Severity: int(d.Severity),
		Message: d.Message,
		File:    d.Range.Start.File,
		SLine:   d.Range.Start.Line,
		SCol:    d.Range.Start.Col,
		SOffset: d.Range.Start.Offset,
		ELine:   d.Range.End.Line,
		ECol:    d.Range.End.Col,
		EOffset: d.Range.End.Offset,
	}
}

func fromWire(w wireDiagnostic) core.Diagnostic {
	return core.Diagnostic{
		RuleID:   w.RuleID,
		Severity: core.Severity(w.Severity),
		Message:  w.Message,
		Range: core.Range{
			Start: core.Position{File: w.File, Line: w.SLine, Col: w.SCol, Offset: w.SOffset},
			End:   core.Position{File: w.File, Line: w.ELine, Col: w.ECol, Offset: w.EOffset},
		},
	}
}

// New initialises the cache. ruleIDs is the sorted list of all currently-active
// rule IDs; if it differs from what was stored on the last run, the results
// directory is wiped before returning.
func New(ruleIDs []string) (*Cache, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	base := filepath.Join(home, ".cache", "eastwood")
	resultsDir := filepath.Join(base, "results")

	rulesetHash := computeRulesetHash(ruleIDs)
	hashFile := filepath.Join(base, "ruleset.hash")

	stored, _ := os.ReadFile(hashFile)
	if strings.TrimSpace(string(stored)) != rulesetHash {
		// Rule set changed — purge all cached results.
		_ = os.RemoveAll(resultsDir)
		_ = os.MkdirAll(base, 0o755)
		_ = os.WriteFile(hashFile, []byte(rulesetHash+"\n"), 0o644)
	}

	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		return nil, err
	}
	return &Cache{resultsDir: resultsDir, rulesetHash: rulesetHash}, nil
}

// Get returns cached diagnostics for a file identified by its content bytes.
// Returns (nil, false) on cache miss or read error.
func (c *Cache) Get(fileBytes []byte) ([]core.Diagnostic, bool) {
	path := c.entryPath(fileBytes)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var entry cachedEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}
	diags := make([]core.Diagnostic, len(entry.Diagnostics))
	for i, w := range entry.Diagnostics {
		diags[i] = fromWire(w)
	}
	return diags, true
}

// Put writes diagnostic results for a file to the cache. Errors are silently
// dropped — a write failure just means the next run gets a cache miss.
func (c *Cache) Put(fileBytes []byte, diags []core.Diagnostic) {
	wire := make([]wireDiagnostic, len(diags))
	for i, d := range diags {
		wire[i] = toWire(d)
	}
	data, err := json.Marshal(cachedEntry{Diagnostics: wire})
	if err != nil {
		return
	}
	_ = os.WriteFile(c.entryPath(fileBytes), data, 0o644)
}

func (c *Cache) entryPath(fileBytes []byte) string {
	h := sha256.New()
	h.Write(fileBytes)
	h.Write([]byte(c.rulesetHash))
	return filepath.Join(c.resultsDir, hex.EncodeToString(h.Sum(nil))+".json")
}

func computeRulesetHash(ruleIDs []string) string {
	sorted := make([]string, len(ruleIDs))
	copy(sorted, ruleIDs)
	sort.Strings(sorted)
	payload := schemaVersion + "\n" + strings.Join(sorted, "\n")
	h := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(h[:])
}

// RuleIDs extracts all rule IDs from a set of analyzers for use with New.
func RuleIDs(analyzers []core.Analyzer) []string {
	var ids []string
	for _, an := range analyzers {
		for _, rule := range an.Rules() {
			ids = append(ids, rule.ID())
		}
	}
	return ids
}

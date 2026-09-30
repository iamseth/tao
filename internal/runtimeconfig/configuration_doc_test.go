package runtimeconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigurationDocCoversRuntimeEnvKeys(t *testing.T) {
	// This doc path is intentional coupling so a moved doc fails loudly.
	path := filepath.Join("..", "..", "docs", "configuration.md")
	content, err := os.ReadFile(path) // #nosec G304 -- fixed repository documentation path, no external input.
	if err != nil {
		t.Fatal(err)
	}

	// Deprecated aliases are documented by pattern, not as individual settings.
	aliasPatterns := make(map[string]string)
	for _, row := range budgetEnvVars {
		if row.aliasOf == "" {
			continue
		}
		switch {
		case strings.HasPrefix(row.name, "TAO_BUDGET_"):
			aliasPatterns[row.name] = "TAO_BUDGET_<SCOPE>_<METRIC>"
		case strings.HasPrefix(row.name, "TAO_MAX_SLICE_"):
			aliasPatterns[row.name] = "TAO_MAX_SLICE_*"
		}
	}

	seen := make(map[string]bool)
	for _, key := range append(RuntimeEnvKeys(), BudgetEnvKeys()...) {
		if seen[key] {
			continue
		}
		seen[key] = true
		token := key
		if pattern, ok := aliasPatterns[key]; ok {
			token = pattern
		}
		if !strings.Contains(string(content), "`"+token+"`") {
			t.Errorf("%s does not document %s as backticked token %q", path, key, token)
		}
	}
}

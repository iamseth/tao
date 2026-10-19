package gitops

import (
	"fmt"
	"os"
	"testing"

	"github.com/iamseth/tao/internal/testenv"
)

func TestMain(m *testing.M) {
	// Fixed identity and dates keep commit-series fingerprints deterministic. Set
	// once here rather than per test so tests can run in parallel.
	for key, value := range map[string]string{
		"GIT_AUTHOR_NAME":    "Tao Test",
		"GIT_AUTHOR_EMAIL":   "tao@example.invalid",
		"GIT_AUTHOR_DATE":    "2026-08-06T00:00:00Z",
		"GIT_COMMITTER_DATE": "2026-08-06T00:00:00Z",
	} {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	cleanup, err := testenv.SetupGit()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	if err := cleanup(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

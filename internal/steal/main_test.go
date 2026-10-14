package steal

import (
	"fmt"
	"os"
	"testing"

	"github.com/iamseth/tao/internal/testenv"
)

func TestMain(m *testing.M) {
	// The runner probe must observe the parent's sanitized environment, not
	// bootstrap a new one. Exit before allocating any bootstrap resources.
	if os.Getenv("TAO_STEAL_TEST_RUNNER") == "1" {
		cwd, err := os.Getwd()
		if err != nil {
			os.Exit(1)
		}
		fmt.Printf("%s|%s|%s", cwd, os.Getenv("GIT_TERMINAL_PROMPT"), os.Getenv("GIT_NO_LAZY_FETCH"))
		os.Exit(0)
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

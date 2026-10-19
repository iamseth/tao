package merge

import (
	"fmt"
	"os"
	"testing"

	"github.com/iamseth/tao/internal/testenv"
)

func TestMain(m *testing.M) {
	cleanup, err := testenv.SetupGit()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	realGitTemplate = testenv.NewRepoTemplate(buildRealGitRepo)
	code := m.Run()
	if err := realGitTemplate.Cleanup(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	if err := cleanup(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

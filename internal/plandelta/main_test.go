package plandelta

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
	code := m.Run()
	if err := cleanup(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

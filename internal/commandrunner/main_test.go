package commandrunner

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/testenv"
)

func TestMain(m *testing.M) {
	// Identity tests assert stable namespaces from fixture tools, not the
	// production deadline (covered via verificationIdentityWithTimeout), and
	// process startup can exceed one second while the whole suite launches.
	verificationIdentityTimeout = 30 * time.Second
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

func TestGitEnvironment(t *testing.T) {
	testenv.AssertGitEnvironment(t)
}

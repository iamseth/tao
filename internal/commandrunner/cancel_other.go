//go:build !unix

package commandrunner

import "os/exec"

func configureCommandCancellation(_ *exec.Cmd) func() { return func() {} }

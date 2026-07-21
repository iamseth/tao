package plan

import verificationimpl "github.com/iamseth/tao/internal/plan/verification"

func verificationCommandFields(command string) []string {
	return verificationimpl.CommandFields(command)
}

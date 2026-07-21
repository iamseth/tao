package cli

import (
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

type envDefaults struct {
	runtimeconfig.EnvDefaults
}

func cliEnvDefaults() (envDefaults, error) {
	defaults, err := runtimeconfig.RuntimeEnvDefaults()
	return envDefaults{EnvDefaults: defaults}, err
}

// runtimeFlagDefaults resolves environment-aware defaults for flag
// registration so command help reflects the same defaults execution applies
// under TAO_* overrides. Invalid env values fall back to the built-in defaults;
// the command path re-resolves them and surfaces the error before running.
func runtimeFlagDefaults() runtimeconfig.EnvDefaults {
	defaults, err := runtimeconfig.RuntimeEnvDefaults()
	if err != nil {
		return runtimeconfig.EnvDefaults{RunOptionsPatch: runtimeconfig.DefaultRunOptionsPatch()}
	}
	return defaults
}

func (d envDefaults) runConfig(overrides runtimeconfig.RunOptionsPatch) (runtimeconfig.Config, error) {
	return runtimeconfig.NewConfigFromStages(d.RunOptionsPatch, overrides)
}

func (d envDefaults) newRunRequest(input string, overrides runtimeconfig.RunOptionsPatch) (run.Request, error) {
	config, err := d.runConfig(overrides)
	if err != nil {
		return run.Request{}, err
	}
	return run.Request{Input: input, ResolvedRunOptions: config.ResolvedOptions()}, nil
}

package run

import "github.com/iamseth/tao/internal/runtimeconfig"

func runtimeEnv(snapshot *runtimeconfig.EnvSnapshot) runtimeconfig.EnvSnapshot {
	if snapshot != nil {
		return *snapshot
	}
	return runtimeconfig.EnvSnapshot{}
}

func (c ExecutionConfig) requireSliceBudgets() error {
	_, err := runtimeEnv(c.RuntimeEnv).Budget()
	return err
}

// executionConfig returns the executor's view of the configuration composed in
// Options.
func (o Options) executionConfig() ExecutionConfig {
	return o.ExecutionConfig
}

// prepareRequestConfig re-applies a resolved request on top of the run-service
// defaults so per-request overrides win. Process-only settings stay on
// ExecutionConfig.
func prepareRequestConfig(defaults ExecutionConfig, request Request) (ExecutionConfig, error) {
	config, err := runtimeconfig.NewConfigFromStages(defaults.RunOptionsPatch(), request.RunOptionsPatch())
	if err != nil {
		return ExecutionConfig{}, err
	}
	// Fields not explicitly overridden inherit from the service defaults by design.
	execution := defaults
	execution.ResolvedRunOptions = config.ResolvedOptions()
	return execution, nil
}

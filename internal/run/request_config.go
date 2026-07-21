package run

import "github.com/iamseth/tao/internal/runtimeconfig"

// executionConfig returns the executor's view of the configuration composed in
// Options.
func (o Options) executionConfig() ExecutionConfig {
	return o.ExecutionConfig
}

// prepareRequestConfig re-applies a resolved request on top of the run-service
// defaults so per-request overrides win. SkipPermissions is process-only and
// stays on ExecutionConfig.
func prepareRequestConfig(defaults ExecutionConfig, request Request) (ExecutionConfig, error) {
	config, err := runtimeconfig.NewConfigFromStages(defaults.RunOptionsPatch(), request.RunOptionsPatch())
	if err != nil {
		return ExecutionConfig{}, err
	}
	execution := ExecutionConfig{ResolvedRunOptions: config.ResolvedOptions()}
	execution.SkipPermissions = defaults.SkipPermissions
	return execution, nil
}

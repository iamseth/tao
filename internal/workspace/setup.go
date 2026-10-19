package workspace

import (
	"context"
	"fmt"
	"io"
	"time"
)

// runWorkspaceSetup preserves the configured command in dependency diagnostics,
// rather than exposing the environment/shell invocation as the command.
func runWorkspaceSetup(ctx context.Context, workspaceRoot, planID, repoRoot, command string, runner CommandRunner, now func() time.Time) (DependencyMetadata, error) {
	metadata, err := runDependencyCommandOutput(ctx, workspaceRoot, "env", []string{
		"TAO_PLAN_ID=" + planID, "TAO_WORKSPACE_PATH=" + workspaceRoot, "TAO_REPO_ROOT=" + repoRoot,
		"sh", "-c", command,
	}, runner, now, io.Discard)
	metadata.Command = command
	if err != nil {
		if len(metadata.FailureReason) > 4096 {
			metadata.FailureReason = metadata.FailureReason[:4096]
		}
		return metadata, fmt.Errorf("workspace setup command failed: %s", metadata.FailureReason)
	}
	return metadata, nil
}

package run

import (
	"context"
	"io"
	"reflect"
	"testing"

	"github.com/iamseth/tao/internal/configtypes"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestPrepareExecutionWorkspaceSetupSnapshot(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(map[bool]string{false: "nil", true: "repository snapshot"}[populated], func(t *testing.T) {
			detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
			detail.State.Repo.Root = t.TempDir()
			detail.State.Workspace = nil
			var calls []string
			fake := runWorkspaceGitFake(&calls)
			count := 0
			input := WorkspaceResolverInput{PlanRecordFactory: memoryPlanRecordFactory}
			const command = `echo "$TAO_PLAN_ID"`
			if populated {
				snapshot := runtimeconfig.ResolveSettings(nil, configtypes.SettingsValues{"workspace.setup": []byte(`"echo \"$TAO_PLAN_ID\""`)}, runtimeconfig.LoadEnv(nil))
				input.Config.RuntimeEnv = &snapshot
			}
			input.CommandRunner = func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if name != "env" {
					return fake(ctx, cwd, name, args, stdout, stderr)
				}
				count++
				want := []string{"TAO_PLAN_ID=" + detail.State.Plan.ID, "TAO_WORKSPACE_PATH=" + cwd, "TAO_REPO_ROOT=" + detail.State.Repo.Root, "sh", "-c", command}
				if !reflect.DeepEqual(args, want) {
					t.Fatalf("args=%q want=%q", args, want)
				}
				return nil
			}
			if _, err := prepareExecutionWorkspace(context.Background(), detail, input); err != nil {
				t.Fatal(err)
			}
			want := 0
			if populated {
				want = 1
			}
			if count != want {
				t.Fatalf("setup calls=%d want=%d", count, want)
			}
		})
	}
}

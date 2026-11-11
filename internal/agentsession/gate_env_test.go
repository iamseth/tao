package agentsession

import (
	"context"
	"io"
	"testing"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agent/process"
	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/testenv"
)

func childCI(ctx context.Context) (string, error) {
	p, err := process.DefaultProcessStarter(ctx, "", "sh", []string{"-c", `printf '%s' "$CI"`})
	if err != nil {
		return "", err
	}
	out, readErr := io.ReadAll(p.Stdout())
	err = p.Wait()
	if readErr != nil {
		return "", readErr
	}
	return string(out), err
}

func TestGateEnvProviderOnly(t *testing.T) {
	testenv.SkipIfShort(t)
	t.Setenv("CI", "inherited")
	control, workspace := t.TempDir(), t.TempDir()
	runGit(t, control, "init")
	runGit(t, control, "commit", "--allow-empty", "-m", "initial")
	for _, pinned := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "pinned"}[pinned], func(t *testing.T) {
			var pins map[string]string
			want := "inherited"
			if pinned {
				pins = map[string]string{"CI": "provider"}
				want = "provider"
			}
			gitCalls, providerCalls := 0, 0
			runner := New(Config{GateEnv: pins, Runtime: runtimeFunc(func(ctx context.Context, _ agent.Session) (agent.SessionResult, error) {
				providerCalls++
				got, err := childCI(ctx)
				if err != nil || got != want {
					t.Fatalf("provider environment %q: %v", got, err)
				}
				return agent.SessionResult{}, nil
			}), CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				gitCalls++
				got, err := childCI(ctx)
				if err != nil || got != "inherited" {
					t.Fatalf("Git context inherited provider pins: %q %v", got, err)
				}
				return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
			}})
			if pinned {
				pins["CI"] = "mutated"
			}
			if _, err := runner.Run(context.Background(), Request{RepoRoot: workspace, ControlRoot: control}); err != nil {
				t.Fatal(err)
			}
			if gitCalls == 0 || providerCalls != 1 {
				t.Fatal(gitCalls, providerCalls)
			}
		})
	}
}

package runtimeconfig

import (
	"os"
	"strings"
	"testing"
)

// Legacy reader tests are replaced by the snapshot's complete-table writes,
// built-ins, empty/zero/presence, boolean grammar, consumption and status tests.
// Exercise invalid syntax for every model role rather than a legacy filter.
func TestSnapshotModelStatusAndConsumption(t *testing.T) {
	for _, key := range []string{EnvModel, EnvRunModel, EnvReviewModel, EnvMergeReviewModel, EnvResolverModel, EnvReworkEscalationModel} {
		for _, value := range []string{"", " \t\u2003", "two models", "two\tmodels", "two\nmodels", "two\u00a0models"} {
			s := snapshotFrom(map[string]string{key: value})
			if err := s.Require(key); err == nil || !strings.HasPrefix(err.Error(), key+":") {
				t.Fatalf("%s=%q: %v", key, value, err)
			}
			for _, row := range s.Status() {
				if row.Name == key && (row.Source != "invalid" || row.Warning == "") {
					t.Fatalf("missing diagnostic: %+v", row)
				}
			}
		}
		s := snapshotFrom(map[string]string{key: "\u2003provider/model\t "})
		if err := s.Require(key); err != nil {
			t.Fatal(err)
		}
		for _, row := range s.Status() {
			if row.Name == key && (row.Value != "provider/model" || row.Source != "env" || row.Warning != "") {
				t.Fatalf("noncanonical model row: %+v", row)
			}
		}
	}
}

func TestSnapshotDefaultsRemainLowestPrecedence(t *testing.T) {
	s := snapshotFrom(map[string]string{EnvAgent: "claude", EnvPullRequest: "true", EnvExecutionMode: "current"})
	overrides := RunOptionsPatch{Agent: AgentPi, ExecutionMode: ExecutionModeIsolated}.WithPullRequest(false)
	resolved, err := ResolveRunOptions(s.Defaults().RunOptionsPatch, overrides)
	if err != nil || resolved.Agent != AgentPi || resolved.PullRequest || resolved.ExecutionMode != ExecutionModeIsolated {
		t.Fatalf("explicit overrides lost: %+v, %v", resolved, err)
	}
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	original, ok := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if ok {
			if err := os.Setenv(name, original); err != nil {
				t.Errorf("restore %s: %v", name, err)
			}
			return
		}
		if err := os.Unsetenv(name); err != nil {
			t.Errorf("unset %s: %v", name, err)
		}
	})
}

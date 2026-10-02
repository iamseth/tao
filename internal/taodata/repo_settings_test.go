package taodata

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRepoSettingsLosslessHelpers(t *testing.T) {
	original := []byte(`{"schema":"tao.repo.v1","id":"repo","name":"repo","root":"/repo","metadata":{"future":true},"run_defaults":{"pull_request":false,"agent":"claude","max_slices":0,"future":{"x":2},"budget":{"slice":{"cost":{"stop":null}}},"models":{"model":"base","run_model":"","future":"keep"}}}`)
	var repo Repo
	if err := json.Unmarshal(original, &repo); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(repo)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	if err := json.Unmarshal(original, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &after); err != nil {
		t.Fatal(err)
	}
	// Ordinary known metadata fields retain their established serialization.
	delete(after, "updated_at")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("round trip: %s", encoded)
	}
	copyRepo := repo.WithPullRequestDefault(nil).WithModelDefaults(RepoModelDefaults{})
	if copyRepo.RunDefaults == nil || string(copyRepo.RunDefaults.Extra["agent"]) != `"claude"` {
		t.Fatal("other defaults cleared")
	}
	copyRepo.RunDefaults.Extra["agent"][1] = 'X'
	if string(repo.RunDefaults.Extra["agent"]) != `"claude"` {
		t.Fatal("helper aliased raw values")
	}
	if pull, ok := repo.PullRequestDefault(); !ok || pull {
		t.Fatal("original pull preference changed")
	}
	if models, _ := repo.ModelDefaults(); models.Base != "base" {
		t.Fatal("original models changed")
	}
	encoded, err = json.Marshal(copyRepo)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["metadata"]) != `{"future":true}` {
		t.Fatal("metadata lost", string(encoded))
	}
	var defaults map[string]json.RawMessage
	if err := json.Unmarshal(raw["run_defaults"], &defaults); err != nil {
		t.Fatal(err)
	}
	if string(defaults["models"]) != `{"future":"keep"}` {
		t.Fatal("future model lost", string(encoded))
	}
}

func TestRepoSettingsInvalidValuesRemainRepairable(t *testing.T) {
	var repo Repo
	const data = `{"run_defaults":{"pull_request":"bad","models":{"model":false},"agent":17}}`
	if err := json.Unmarshal([]byte(data), &repo); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(repo.RunDefaults)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"pull_request":"bad","models":{"model":false},"agent":17}`), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("invalid values lost: %s", raw)
	}
	repo = repo.WithPullRequestDefault(nil)
	if _, exists := repo.RunDefaults.Extra["pull_request"]; exists {
		t.Fatal("bad field not removed")
	}
}

func TestRegisterCurrentPreservesAdditionalDefaults(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	t.Chdir(root)
	registry := NewRegistry(t.TempDir())
	repo, err := registry.RegisterCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(registry.DataHome, "repos", repo.ID, "repo.json")
	data, err := os.ReadFile(path) //nolint:gosec // test-owned temporary registry
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["custom"] = json.RawMessage(`{"future":true}`)
	raw["run_defaults"] = json.RawMessage(`{"agent":"claude","max_slices":5,"future":{"keep":1}}`)
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RegisterCurrent(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path) //nolint:gosec // test-owned temporary registry
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]json.RawMessage
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"custom", "run_defaults"} {
		var a, b any
		if err := json.Unmarshal(raw[key], &a); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(after[key], &b); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("registration lost %s: %s", key, data)
		}
	}
}

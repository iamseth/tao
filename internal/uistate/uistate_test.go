package uistate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "nested", "tao")
	store := Store{DataHome: dataHome}
	if got, want := store.Path(), filepath.Join(dataHome, "ui-filters.json"); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
	want := Filters{Version: 1, Enabled: true, Repositories: []string{"repo-b", "repo-a"}, Statuses: []string{"planned", "completed"}, Tags: []string{"tier1", "bug"}}
	for _, enabled := range []bool{true, false} {
		want.Enabled = enabled
		if err := store.Save(want); err != nil {
			t.Fatal(err)
		}
		got, err := (Store{DataHome: dataHome}).Load()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Load() = %+v, %v, want %+v", got, err, want)
		}
		info, err := os.Stat(store.Path())
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("permissions = %o, want 600", got)
		}
	}
}

func TestStoreMissingFile(t *testing.T) {
	store := Store{DataHome: filepath.Join(t.TempDir(), "missing")}
	got, err := store.Load()
	if err != nil || !reflect.DeepEqual(got, Filters{}) {
		t.Fatalf("Load() = %+v, %v, want defaults without error", got, err)
	}
	if _, err := os.Stat(store.DataHome); !os.IsNotExist(err) {
		t.Fatalf("Load created data home: %v", err)
	}
}

func TestStoreCorruptFile(t *testing.T) {
	for _, content := range []string{`{`, `{"enabled":true,"statuses":42}`, `{"enabled":true} trailing`} {
		t.Run(content, func(t *testing.T) {
			store := Store{DataHome: t.TempDir()}
			if err := os.WriteFile(store.Path(), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := store.Load()
			if err == nil || !strings.Contains(err.Error(), "decode UI filters") || !strings.Contains(err.Error(), store.Path()) {
				t.Fatalf("Load error = %v, want descriptive decode error", err)
			}
			if !reflect.DeepEqual(got, Filters{}) {
				t.Fatalf("Load returned partial filters: %+v", got)
			}
		})
	}
}

func TestStoreVersionField(t *testing.T) {
	store := Store{DataHome: t.TempDir()}
	if err := store.Save(Filters{Version: 1}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content, &fields); err != nil {
		t.Fatal(err)
	}
	if got := string(fields["version"]); got != "1" {
		t.Fatalf("JSON version = %q, want 1", got)
	}
	if err := os.WriteFile(store.Path(), []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || got.Version != 2 {
		t.Fatalf("Load() = %+v, %v, want stored version 2", got, err)
	}
}

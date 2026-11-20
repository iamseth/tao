package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestObserveFinalVerificationPrerequisites(t *testing.T) {
	root := t.TempDir()
	put := func(name, text string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), mode); err != nil {
			t.Fatal(err)
		}
	}
	put("input", "first", 0600)
	p := []plan.FinalVerificationPrerequisite{{Kind: "file", Path: "input"}}
	first, err := observeFinalVerificationPrerequisites(context.Background(), root, "", p)
	if err != nil || len(first) != 1 || first[0].Identity != "input" || len(first[0].Digest) != 64 {
		t.Fatalf("observation: %+v, %v", first, err)
	}
	put("input", "second", 0600)
	second, err := observeFinalVerificationPrerequisites(context.Background(), root, "", p)
	if err != nil || second[0].Digest == first[0].Digest {
		t.Fatalf("changed content: %+v, %v", second, err)
	}
	empty, err := observeFinalVerificationPrerequisites(context.Background(), root, "", nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty: %+v, %v", empty, err)
	}
	for _, tc := range []struct {
		name string
		p    plan.FinalVerificationPrerequisite
		want error
	}{
		{"missing", plan.FinalVerificationPrerequisite{Kind: "file", Path: "absent"}, ErrFinalPrerequisiteMissing},
		{"traversal", plan.FinalVerificationPrerequisite{Kind: "file", Path: "../input"}, ErrFinalPrerequisiteUnsupported},
		{"absolute", plan.FinalVerificationPrerequisite{Kind: "file", Path: filepath.Join(root, "input")}, ErrFinalPrerequisiteUnsupported},
		{"directory", plan.FinalVerificationPrerequisite{Kind: "file", Path: "."}, ErrFinalPrerequisiteUnsupported},
		{"unsupported", plan.FinalVerificationPrerequisite{Kind: "service", Name: "secret"}, ErrFinalPrerequisiteUnsupported},
		{"missing executable", plan.FinalVerificationPrerequisite{Kind: "executable", Name: "absent"}, ErrFinalPrerequisiteMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := observeFinalVerificationPrerequisites(context.Background(), root, root, append(p, tc.p))
			if !errors.Is(err, tc.want) || got != nil {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := observeFinalVerificationPrerequisites(ctx, root, "", p); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestObserveFinalVerificationPrerequisitesSymlinksAndBounds(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "input"), []byte("not observable"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, target string
		want         error
	}{
		{"escape", filepath.Join(outside, "input"), ErrFinalPrerequisiteUnsupported},
		{"dangling", "absent", ErrFinalPrerequisiteMissing},
		{"loop", "loop", ErrFinalPrerequisiteUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.Symlink(tc.target, filepath.Join(root, tc.name)); err != nil {
				t.Fatal(err)
			}
			got, err := observeFinalVerificationPrerequisites(context.Background(), root, "", []plan.FinalVerificationPrerequisite{{Kind: "file", Path: tc.name}})
			if got != nil || !errors.Is(err, tc.want) {
				t.Fatalf("%+v, %v", got, err)
			}
		})
	}
	f, err := os.Create(filepath.Join(root, "large")) // #nosec G304 -- fixed name in isolated test directory.
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(finalPrerequisiteMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := observeFinalVerificationPrerequisites(context.Background(), root, "", []plan.FinalVerificationPrerequisite{{Kind: "file", Path: "large"}}); !errors.Is(err, ErrFinalPrerequisiteUnsupported) {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "input"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("input", filepath.Join(root, "inside")); err != nil {
		t.Fatal(err)
	}
	if _, err := observeFinalVerificationPrerequisites(context.Background(), root, "", []plan.FinalVerificationPrerequisite{{Kind: "file", Path: "inside"}}); err != nil {
		t.Fatal(err)
	}
}

func TestObserveFinalVerificationPrerequisitesExecutable(t *testing.T) {
	root, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	// Deliberately invalid program bytes: observation must not execute them.
	for _, dir := range []string{a, b} {
		if err := os.WriteFile(filepath.Join(dir, "tool"), []byte("not a program"), 0700); err != nil { // #nosec G306 -- executable mode is the prerequisite under test.
			t.Fatal(err)
		}
	}
	p := []plan.FinalVerificationPrerequisite{{Kind: "executable", Name: "tool"}}
	first, err := observeFinalVerificationPrerequisites(context.Background(), root, a, p)
	if err != nil {
		t.Fatal(err)
	}
	second, err := observeFinalVerificationPrerequisites(context.Background(), root, b, p)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Identity == second[0].Identity || first[0].Digest != second[0].Digest {
		t.Fatalf("PATH identity: %+v %+v", first, second)
	}
	if err := os.WriteFile(filepath.Join(a, "tool"), []byte("substituted"), 0700); err != nil { // #nosec G306 -- executable mode is the prerequisite under test.
		t.Fatal(err)
	}
	third, err := observeFinalVerificationPrerequisites(context.Background(), root, a, p)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Digest == third[0].Digest {
		t.Fatal("substitution not observed")
	}
	if err := os.Chmod(filepath.Join(a, "tool"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := observeFinalVerificationPrerequisites(context.Background(), root, a, p); !errors.Is(err, ErrFinalPrerequisiteMissing) {
		t.Fatal(err)
	}
	for _, name := range []string{"../tool", "/tool", "a/tool", ""} {
		if _, err := observeFinalVerificationPrerequisites(context.Background(), root, a, []plan.FinalVerificationPrerequisite{{Kind: "executable", Name: name}}); !errors.Is(err, ErrFinalPrerequisiteUnsupported) {
			t.Fatalf("%q: %v", name, err)
		}
	}
}

func TestObserveFinalVerificationPrerequisitesPermission(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private")
	if err := os.WriteFile(path, []byte("private contents"), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0600) })
	if f, err := os.Open(path); err == nil { // #nosec G304 -- fixed permission-test fixture in isolated directory.
		_ = f.Close()
		t.Skip("host bypasses permission bits")
	}
	got, err := observeFinalVerificationPrerequisites(context.Background(), root, "", []plan.FinalVerificationPrerequisite{{Kind: "file", Path: "private"}})
	if got != nil || !errors.Is(err, ErrFinalPrerequisiteUnsupported) {
		t.Fatalf("%+v, %v", got, err)
	}
}

// A context hook makes mutation/cancellation deterministic at the first read,
// after the observer has opened and statted the file. No timing race is needed.
type prerequisiteReadContext struct {
	context.Context
	onRead func()
	calls  int
}

func (c *prerequisiteReadContext) Err() error {
	c.calls++
	if c.calls == 3 {
		c.onRead()
	}
	return c.Context.Err()
}

func TestObserveFinalVerificationPrerequisitesDuringRead(t *testing.T) {
	for _, action := range []string{"change", "replace", "cancel"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "input")
			if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &prerequisiteReadContext{Context: parent, onRead: func() {
				switch action {
				case "change":
					if err := os.WriteFile(path, []byte("changed size"), 0600); err != nil {
						t.Fatal(err)
					}
				case "replace":
					if err := os.Rename(path, path+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
						t.Fatal(err)
					}
				case "cancel":
					cancel()
				}
			}}
			want := ErrFinalPrerequisiteUnstable
			if action == "cancel" {
				want = context.Canceled
			}
			got, err := observeFinalVerificationPrerequisites(ctx, root, "", []plan.FinalVerificationPrerequisite{{Kind: "file", Path: "input"}})
			if got != nil || !errors.Is(err, want) {
				t.Fatalf("%+v, %v", got, err)
			}
		})
	}
}

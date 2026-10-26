package repoadmission

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/testenv"
)

func TestOwnershipGapHelper(t *testing.T) {
	if os.Getenv("TAO_ADMISSION_HELPER") != "gap" {
		return
	}
	lock, err := plan.AcquireRunLock(os.Getenv("TAO_ADMISSION_PLAN"), "first", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	err = With(context.Background(), Request{Root: os.Getenv("TAO_ADMISSION_ROOT"), Mode: Mode(os.Getenv("TAO_ADMISSION_MODE"))}, func(context.Context) error {
		fmt.Println("ready")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOwnershipGap(t *testing.T) {
	testenv.SkipIfShort(t)
	first, second := t.TempDir(), t.TempDir()
	root := newRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOwnershipGapHelper$") // #nosec G204 G702 -- executes this test binary with a fixed helper selector.
	cmd.Env = append(os.Environ(), "TAO_ADMISSION_HELPER=gap", "TAO_ADMISSION_PLAN="+first, "TAO_ADMISSION_ROOT="+root, "TAO_ADMISSION_MODE=run")
	input, _ := cmd.StdinPipe()
	output, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = cmd.Wait() }()
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("barrier: %q %v", line, err)
	}
	lock, err := plan.AcquireRunLock(second, "second", time.Now())
	if err != nil {
		t.Fatalf("different plan lock unexpectedly excluded overlap: %v", err)
	}
	defer func() { _ = lock.Release() }()
	err = With(context.Background(), Request{Root: root, Mode: Merge}, func(context.Context) error { t.Fatal("merge overlapped run"); return nil })
	var busy *ContentionError
	if !errors.As(err, &busy) {
		t.Fatalf("expected repository contention, got %v", err)
	}
	if err := With(context.Background(), Request{Root: root, Mode: Run}, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if err := With(context.Background(), Request{Root: root, Mode: Merge}, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("process death retained lease: %v", err)
	}
}

func TestSimultaneousHelper(t *testing.T) {
	if os.Getenv("TAO_ADMISSION_HELPER") != "simultaneous" {
		return
	}
	input := bufio.NewReader(os.Stdin)
	fmt.Println("ready")
	_, _ = input.ReadString('\n')
	err := With(context.Background(), Request{Root: os.Getenv("TAO_ADMISSION_ROOT"), Mode: Mode(os.Getenv("TAO_ADMISSION_MODE"))}, func(context.Context) error {
		fmt.Println("held")
		_, _ = input.ReadString('\n')
		return nil
	})
	if err != nil {
		var busy *ContentionError
		if !errors.As(err, &busy) {
			t.Fatal(err)
		}
		fmt.Println("busy")
	}
}

func TestSimultaneousProcesses(t *testing.T) {
	testenv.SkipIfShort(t)
	for _, modes := range [][2]Mode{{Run, Merge}, {Merge, Run}, {Run, Run}, {Merge, Merge}} {
		t.Run(string(modes[0])+"-"+string(modes[1]), func(t *testing.T) {
			root := newRepo(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			type child struct {
				send    func()
				receive func() string
			}
			children := make([]child, 0, 2)
			for _, mode := range modes {
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSimultaneousHelper$") // #nosec G204 G702 -- executes this test binary with a fixed helper selector.
				cmd.Env = append(os.Environ(), "TAO_ADMISSION_HELPER=simultaneous", "TAO_ADMISSION_ROOT="+root, "TAO_ADMISSION_MODE="+string(mode))
				input, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				output, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				cmd.Stderr = os.Stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = input.Close(); _ = cmd.Wait() })
				reader := bufio.NewReader(output)
				c := child{send: func() { _, _ = fmt.Fprintln(input, "go") }, receive: func() string {
					line, err := reader.ReadString('\n')
					if err != nil {
						t.Fatal(err)
					}
					return line
				}}
				if line := c.receive(); line != "ready\n" {
					t.Fatalf("barrier %q", line)
				}
				children = append(children, c)
			}
			for _, c := range children {
				c.send()
			}
			held := 0
			for _, c := range children {
				if line := c.receive(); line == "held\n" {
					held++
				} else if line != "busy\n" {
					t.Fatalf("outcome %q", line)
				}
			}
			want := 1
			if modes[0] == Run && modes[1] == Run {
				want = 2
			}
			if held != want {
				t.Fatalf("simultaneous holders %d, want %d", held, want)
			}
			for _, c := range children {
				c.send()
			}
		})
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "-q")
	return root
}
func git(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...) // #nosec G204 -- test-owned Git arguments and temporary repositories.
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git: %v %s", err, out)
	}
}

func TestDescriptorNotInherited(t *testing.T) {
	testenv.SkipIfShort(t)
	root := newRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSimultaneousHelper$") // #nosec G204 G702 -- executes this test binary with a fixed helper selector.
	cmd.Env = append(os.Environ(), "TAO_ADMISSION_HELPER=simultaneous", "TAO_ADMISSION_ROOT="+root, "TAO_ADMISSION_MODE=run")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	err = With(ctx, Request{Root: root, Mode: Merge}, func(context.Context) error {
		if err := cmd.Start(); err != nil {
			return err
		}
		t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
		line, err := bufio.NewReader(output).ReadString('\n')
		if err != nil {
			return err
		}
		if line != "ready\n" {
			return fmt.Errorf("barrier: %q", line)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := With(ctx, Request{Root: root, Mode: Merge}, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("spawned process retained descriptor: %v", err)
	}
}

func TestAdmission(t *testing.T) {
	root, other := newRepo(t), newRepo(t)
	ctx := context.Background()
	noop := func(context.Context) error { return nil }
	for _, mode := range []Mode{Run, Merge} {
		t.Run(string(mode), func(t *testing.T) {
			var saved context.Context
			err := With(ctx, Request{Root: root, Mode: mode}, func(owned context.Context) error {
				saved = owned
				if err := With(owned, Request{Root: root, Mode: mode}, noop); err != nil {
					t.Fatal(err)
				}
				opposite := Run
				if mode == Run {
					opposite = Merge
				}
				if err := With(owned, Request{Root: root, Mode: opposite}, noop); err == nil {
					t.Fatal("nested mode change accepted")
				}
				if err := With(owned, Request{Root: other, Mode: mode}, noop); err == nil {
					t.Fatal("nested repository change accepted")
				}
				for _, contender := range []Mode{Run, Merge} {
					err := With(ctx, Request{Root: root, Mode: contender}, noop)
					if mode == Run && contender == Run {
						if err != nil {
							t.Fatal(err)
						}
						continue
					}
					var busy *ContentionError
					if !errors.As(err, &busy) {
						t.Fatalf("expected contention: %v", err)
					}
				}
				return With(ctx, Request{Root: other, Mode: Merge}, noop)
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := With(saved, Request{Root: root, Mode: mode}, noop); err == nil {
				t.Fatal("expired ownership reused")
			}
		})
	}
	path := filepath.Join(root, ".git", "tao-admission.lock")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("callback")
	if err := With(ctx, Request{Root: root, Mode: Merge}, func(context.Context) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	err = With(canceled, Request{Root: root, Mode: Merge}, func(ctx context.Context) error { cancel(); return ctx.Err() })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := With(canceled, Request{Root: root, Mode: Run}, noop); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := With(ctx, Request{Root: root, Mode: Merge}, noop); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("lock inode replaced")
	}
	if err := With(ctx, Request{Root: root, Mode: "invalid"}, noop); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := With(ctx, Request{Root: root, Mode: Run}, noop); err == nil {
		t.Fatal("open error ignored")
	}
}

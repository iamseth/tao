package runqueue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"testing"
)

func TestNotifyBatchCompleteWhitespaceCommandNoop(t *testing.T) {
	called := false
	warned := false
	NotifyBatchComplete(context.Background(), " \n\t ", BatchSummary{}, func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		called = true
		return nil
	}, io.Discard, func(string, ...any) { warned = true })

	if called {
		t.Fatal("expected whitespace notify command not to run")
	}
	if warned {
		t.Fatal("expected whitespace notify command not to warn")
	}
}

func TestNotifyBatchCompleteSetsAndRestoresEnv(t *testing.T) {
	preserveBatchEnv(t)
	setEnvForNotifyTest(t, envBatchTotal, "preexisting-total")
	setEnvForNotifyTest(t, envBatchFailed, "preexisting-failed")

	summary := BatchSummary{
		Total:             5,
		Statuses:          QueueStatusCounts{Succeeded: 2, Failed: 1, Pending: 2},
		SucceededReviewed: 1,
	}
	var gotEnv map[string]string
	NotifyBatchComplete(context.Background(), "notify tao", summary, func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("expected notify command context to have a deadline")
		}
		gotEnv = batchEnvValues()
		return nil
	}, io.Discard, func(format string, args ...any) {
		t.Fatalf("unexpected warning: %s", fmt.Sprintf(format, args...))
	})

	if wantEnv := BatchSummaryEnv(summary); !reflect.DeepEqual(gotEnv, wantEnv) {
		t.Fatalf("notify env = %+v, want %+v", gotEnv, wantEnv)
	}
	assertEnvValue(t, envBatchTotal, "preexisting-total")
	assertEnvValue(t, envBatchFailed, "preexisting-failed")
	assertEnvUnset(t, envBatchSucceeded)
	assertEnvUnset(t, envBatchReviewed)
	assertEnvUnset(t, envBatchPending)
}

func TestNotifyBatchCompleteFailureWarnsAndDoesNotFail(t *testing.T) {
	var gotStderr io.Writer
	var warnings []string
	NotifyBatchComplete(context.Background(), "notify tao", BatchSummary{}, func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		gotStderr = stderr
		return errors.New("notify boom")
	}, nil, func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})

	if gotStderr != io.Discard {
		t.Fatalf("notify stderr = %T, want io.Discard", gotStderr)
	}
	if want := []string{"TAO_NOTIFY_COMMAND failed: notify boom"}; !reflect.DeepEqual(warnings, want) {
		t.Fatalf("warnings = %v, want %v", warnings, want)
	}
}

func TestNotifyBatchCompleteRunsShellCommand(t *testing.T) {
	var stderr bytes.Buffer
	var gotCWD string
	var gotName string
	var gotArgs []string
	var gotStdout io.Writer
	var gotStderr io.Writer
	NotifyBatchComplete(context.Background(), "  notify --message tao  ", BatchSummary{}, func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		gotCWD = cwd
		gotName = name
		gotArgs = append([]string(nil), args...)
		gotStdout = stdout
		gotStderr = stderr
		return nil
	}, &stderr, func(format string, args ...any) {
		t.Fatalf("unexpected warning: %s", fmt.Sprintf(format, args...))
	})

	if gotCWD != "" || gotName != "sh" || !reflect.DeepEqual(gotArgs, []string{"-c", "notify --message tao"}) {
		t.Fatalf("notify command = cwd %q name %q args %v", gotCWD, gotName, gotArgs)
	}
	if gotStdout != io.Discard {
		t.Fatalf("notify stdout = %T, want io.Discard", gotStdout)
	}
	if gotStderr != &stderr {
		t.Fatalf("notify stderr = %T, want caller stderr", gotStderr)
	}
}

func preserveBatchEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{envBatchTotal, envBatchSucceeded, envBatchReviewed, envBatchFailed, envBatchPending} {
		value, exists := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(name, value)
				return
			}
			_ = os.Unsetenv(name)
		})
	}
}

func batchEnvValues() map[string]string {
	return map[string]string{
		envBatchTotal:     os.Getenv(envBatchTotal),
		envBatchSucceeded: os.Getenv(envBatchSucceeded),
		envBatchReviewed:  os.Getenv(envBatchReviewed),
		envBatchFailed:    os.Getenv(envBatchFailed),
		envBatchPending:   os.Getenv(envBatchPending),
	}
}

func setEnvForNotifyTest(t *testing.T, name string, value string) {
	t.Helper()
	if err := os.Setenv(name, value); err != nil {
		t.Fatal(err)
	}
}

func assertEnvValue(t *testing.T, name string, value string) {
	t.Helper()
	got, ok := os.LookupEnv(name)
	if !ok || got != value {
		t.Fatalf("%s = %q, %v; want %q, true", name, got, ok, value)
	}
}

func assertEnvUnset(t *testing.T, name string) {
	t.Helper()
	if got, ok := os.LookupEnv(name); ok {
		t.Fatalf("%s = %q, want unset", name, got)
	}
}

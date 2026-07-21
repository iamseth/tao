package runqueue

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
)

const queueNotifyTimeout = 5 * time.Second

const (
	envBatchTotal     = "TAO_BATCH_TOTAL"
	envBatchSucceeded = "TAO_BATCH_SUCCEEDED"
	envBatchReviewed  = "TAO_BATCH_REVIEWED"
	envBatchFailed    = "TAO_BATCH_FAILED"
	envBatchPending   = "TAO_BATCH_PENDING"
)

// NotifyBatchComplete runs command after a queue drain with batch summary
// environment variables. Failures are reported through warnf only.
func NotifyBatchComplete(ctx context.Context, command string, summary BatchSummary, runner commandrunner.Runner, stderr io.Writer, warnf func(string, ...any)) {
	command = strings.TrimSpace(command)
	if command == "" {
		return
	}
	notifyCtx, cancel := context.WithTimeout(ctx, queueNotifyTimeout)
	defer cancel()
	if runner == nil {
		runner = commandrunner.DefaultLocal
	}
	if stderr == nil {
		stderr = io.Discard
	}
	err := withTemporaryEnv(BatchSummaryEnv(summary), func() error {
		return runner(notifyCtx, "", "sh", []string{"-c", command}, io.Discard, stderr)
	})
	if err != nil && warnf != nil {
		warnf("TAO_NOTIFY_COMMAND failed: %v", err)
	}
}

// BatchSummaryEnv returns the TAO_BATCH_* environment values for summary.
func BatchSummaryEnv(summary BatchSummary) map[string]string {
	return map[string]string{
		envBatchTotal:     fmt.Sprint(summary.Total),
		envBatchSucceeded: fmt.Sprint(summary.Statuses.Succeeded),
		envBatchReviewed:  fmt.Sprint(summary.SucceededReviewed),
		envBatchFailed:    fmt.Sprint(summary.Statuses.Failed),
		envBatchPending:   fmt.Sprint(summary.Statuses.Pending),
	}
}

func withTemporaryEnv(values map[string]string, fn func() error) error {
	type restore struct {
		name   string
		value  string
		exists bool
	}
	restores := make([]restore, 0, len(values))
	defer func() {
		for _, item := range slices.Backward(restores) {

			if item.exists {
				_ = os.Setenv(item.name, item.value)
				continue
			}
			_ = os.Unsetenv(item.name)
		}
	}()
	for name, value := range values {
		previous, exists := os.LookupEnv(name)
		restores = append(restores, restore{name: name, value: previous, exists: exists})
		if err := os.Setenv(name, value); err != nil {
			return err
		}
	}
	return fn()
}

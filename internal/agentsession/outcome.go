package agentsession

import (
	"errors"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/agent"
)

// ResultText returns trimmed final text, falling back to trimmed output.
func ResultText(result Result) string {
	if text := strings.TrimSpace(result.FinalText); text != "" {
		return text
	}
	return strings.TrimSpace(result.Output)
}

// Outcome summarizes provider-neutral session text, timeout, and warning policy.
// Domain adapters retain ownership of telemetry projection and persistence.
type Outcome struct {
	Text           string
	TimedOut       bool
	TimeoutSeconds int64
	ReportWarning  bool
	WarningMessage string
	MetricsUsable  bool
}

// Summarize preserves the result's warning policy and classifies session timeouts.
// TimeoutSeconds is truncated to whole seconds and floored to 1 because merge
// batch timeout events require a positive duration and run session timeouts are
// never sub-second. It is zero for non-timeout outcomes.
func Summarize(result Result, err error) Outcome {
	outcome := Outcome{
		Text:           ResultText(result),
		ReportWarning:  result.ReportMetricsWarning,
		WarningMessage: result.MetricsWarningMessage,
		MetricsUsable:  result.MetricsUsable,
	}
	var timeoutErr *agent.SessionTimeoutError
	if errors.As(err, &timeoutErr) {
		outcome.TimedOut = true
		outcome.TimeoutSeconds = max(1, int64(timeoutErr.Timeout/time.Second))
	}
	return outcome
}

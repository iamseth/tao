package agentsession

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
)

func TestResultText(t *testing.T) {
	for _, tt := range []struct {
		name   string
		result Result
		want   string
	}{
		{"final text preferred", Result{FinalText: " \nfinal\t ", Output: " output "}, "final"},
		{"empty final text", Result{Output: " \noutput\t "}, "output"},
		{"whitespace final text", Result{FinalText: " \n\t", Output: " output "}, "output"},
		{"both empty", Result{}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResultText(tt.result); got != tt.want {
				t.Fatalf("ResultText() = %q, want %q", got, tt.want)
			}
			if got := Summarize(tt.result, nil).Text; got != tt.want {
				t.Fatalf("Summarize().Text = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummarizeTimeout(t *testing.T) {
	for _, tt := range []struct {
		name     string
		err      error
		timedOut bool
		seconds  int64
	}{
		{"success", nil, false, 0},
		{"ordinary error", errors.New("provider failed"), false, 0},
		{"context deadline", context.DeadlineExceeded, false, 0},
		{"subsecond", &agent.SessionTimeoutError{Timeout: 500 * time.Millisecond}, true, 1},
		{"zero", &agent.SessionTimeoutError{}, true, 1},
		{"whole seconds", &agent.SessionTimeoutError{Timeout: 95 * time.Second}, true, 95},
		{"fractional seconds", &agent.SessionTimeoutError{Timeout: 2599 * time.Millisecond}, true, 2},
		{"wrapped", fmt.Errorf("session: %w", &agent.SessionTimeoutError{Timeout: 500 * time.Millisecond}), true, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Summarize(Result{Output: " partial "}, tt.err)
			if got.TimedOut != tt.timedOut || got.TimeoutSeconds != tt.seconds || got.Text != "partial" {
				t.Fatalf("Summarize() = %+v, want TimedOut=%v TimeoutSeconds=%d and partial text", got, tt.timedOut, tt.seconds)
			}
		})
	}
}

func TestSummarizeMetricsWarningPolicy(t *testing.T) {
	for _, report := range []bool{false, true} {
		for _, usable := range []bool{false, true} {
			result := Result{ReportMetricsWarning: report, MetricsWarningMessage: "collect: unavailable", MetricsUsable: usable}
			got := Summarize(result, nil)
			if got.ReportWarning != report || got.WarningMessage != result.MetricsWarningMessage || got.MetricsUsable != usable {
				t.Fatalf("Summarize(%+v) = %+v, want preserved warning policy", result, got)
			}
		}
	}
}

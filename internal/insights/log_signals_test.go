package insights

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent/claude"
	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/agent/process"
	"github.com/iamseth/tao/internal/plan"
)

func TestAggregateExtractsRecentExecutionSignalsAndRedactsEvidence(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	awsAccessVariable := "AWS_ACCESS_" + "KEY_ID"
	awsAccessKey := "AKIA" + "IOSFODNN7EXAMPLE"
	awsSessionVariable := "AWS_SESSION_" + "TOKEN"
	awsSessionToken := "session-" + "secret"
	basicPassword := "basic-" + "secret"
	command := fmt.Sprintf("%s=%s %s=%s curl -u user:%s -H 'Authorization: Bearer top-secret' 'https://user:password@api.example.com/v1/accounts?api_key=also-secret' && jq --version", awsAccessVariable, awsAccessKey, awsSessionVariable, awsSessionToken, basicPassword)
	first := writeLogPlan(t,
		logrecord.Record{Type: logrecord.TypeAssistant, Content: "copied example: → bash {\"command\":\"made-up-tool\"}\nsource text says bash: copied-tool: command not found"},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":` + fmt.Sprintf("%q", command) + `}`},
		logrecord.Record{Type: logrecord.TypeToolResult, Name: "bash", Content: "bash: jq: command not found", Failed: true},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "read", Payload: `{"path":"README.md"}`},
		logrecord.Record{Type: logrecord.TypeToolResult, Name: "read"},
	)
	second := writeLogPlan(t, logrecord.Record{Type: logrecord.TypeToolCall, Name: "shell", Payload: `{"cmd":"curl https://api.example.com/health && gh issue list"}`})

	report := aggregateLogsAt(t, now, []plan.PlanSummary{
		{ID: "one", Dir: first, LastActivityAt: new(now.Add(-time.Hour))},
		{ID: "two", Dir: second, LastActivityAt: new(now.Add(-24 * time.Hour))},
	})
	if report.RecentLogs.Coverage.Eligible != 2 || report.RecentLogs.Coverage.Scanned != 2 {
		t.Fatalf("coverage = %#v", report.RecentLogs.Coverage)
	}
	checks := []struct {
		signals []LogSignal
		name    string
		count   int
		plans   int
	}{
		{report.RecentLogs.MissingExecutables, "jq", 1, 1},
		{report.RecentLogs.ToolUses, "curl", 2, 2},
		{report.RecentLogs.ToolUses, "read", 1, 1},
		{report.RecentLogs.ExternalSystems, "api.example.com", 2, 2},
	}
	for _, check := range checks {
		signal := findLogSignal(check.signals, check.name)
		if signal == nil || signal.Count != check.count || signal.PlanCount != check.plans || signal.RepositoryCount != 1 {
			t.Fatalf("signal %q = %#v in %#v", check.name, signal, check.signals)
		}
	}
	if findLogSignal(report.RecentLogs.MissingExecutables, "copied-tool") != nil || findLogSignal(report.RecentLogs.ToolUses, "made-up-tool") != nil {
		t.Fatalf("copied text produced execution evidence: %#v", report.RecentLogs)
	}

	encoded, err := json.Marshal(report.RecentLogs)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range []string{awsAccessKey, awsSessionToken, basicPassword, "top-secret", "also-secret", "password@", "/v1/accounts", "?api_key", "Authorization", awsAccessVariable, "curl -u"} {
		if strings.Contains(text, secret) {
			t.Fatalf("recent log report leaked %q: %s", secret, text)
		}
	}
	for _, want := range []string{"tool: curl", "external system: api.example.com", "missing executable: jq"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected synthetic exemplar %q: %s", want, text)
		}
	}
}

func TestAggregateExtractsMissingExecutableFromClaudeStream(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	stream := strings.NewReader(strings.Join([]string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"copy-1","name":"Bash","input":{"command":"cat errors.txt"}},{"type":"tool_use","id":"fail-1","name":"Bash","input":{"command":"missing-claude-tool --version"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"copy-1","content":"missing-claude-tool: command not found","is_error":false},{"type":"tool_result","tool_use_id":"fail-1","content":"missing-claude-tool: command not found","is_error":true}]}}`,
		`{"type":"result","subtype":"success"}`,
	}, "\n") + "\n")
	dir := t.TempDir()
	log, err := os.Create(filepath.Join(dir, "agent-run.log")) // #nosec G304 -- test temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	if err := logrecord.Write(log, logrecord.Record{Type: logrecord.TypeSession, Content: "test", Timestamp: now.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	client := claude.Client{
		Log: log,
		ProcessStarter: func(context.Context, string, string, []string) (process.Process, error) {
			return &streamFixtureProcess{stdout: stream}, nil
		},
	}
	if _, err := client.RunAgentSession(context.Background(), claude.Request{Prompt: "inspect environment"}); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	report := aggregateLogsAt(t, now, []plan.PlanSummary{{ID: "claude", Dir: dir, LastActivityAt: new(now)}})
	missing := findLogSignal(report.RecentLogs.MissingExecutables, "missing-claude-tool")
	if missing == nil || missing.Count != 1 || missing.PlanCount != 1 {
		t.Fatalf("missing executable = %#v in %#v", missing, report.RecentLogs.MissingExecutables)
	}
	if report.RecentLogs.Coverage.Scanned != 1 {
		t.Fatalf("coverage = %#v", report.RecentLogs.Coverage)
	}
}

func TestAggregateFiltersSessionsOutsideRecentLogWindow(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	log := writeFramedLogPlan(t,
		logrecord.Record{Type: logrecord.TypeSession, Content: "old", Timestamp: now.Add(-31 * 24 * time.Hour).Format(time.RFC3339)},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"old-tool"}`},
		logrecord.Record{Type: logrecord.TypeToolResult, Name: "bash", Content: "bash: old-tool: command not found", Failed: true},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"curl https://old.example.com/private"}`},
		logrecord.Record{Type: logrecord.TypeSession, Content: "recent", Timestamp: now.Add(-time.Hour).Format(time.RFC3339)},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"recent-tool"}`},
		logrecord.Record{Type: logrecord.TypeToolResult, Name: "bash", Content: "bash: recent-tool: command not found", Failed: true},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"curl https://recent.example.com/status"}`},
	)

	report := aggregateLogsAt(t, now, []plan.PlanSummary{
		{ID: "recently-active", Dir: log, LastActivityAt: new(now)},
	})
	if findLogSignal(report.RecentLogs.ToolUses, "old-tool") != nil || findLogSignal(report.RecentLogs.MissingExecutables, "old-tool") != nil || findLogSignal(report.RecentLogs.ExternalSystems, "old.example.com") != nil {
		t.Fatalf("old session produced evidence: %#v", report.RecentLogs)
	}
	if findLogSignal(report.RecentLogs.ToolUses, "recent-tool") == nil || findLogSignal(report.RecentLogs.MissingExecutables, "recent-tool") == nil || findLogSignal(report.RecentLogs.ExternalSystems, "recent.example.com") == nil {
		t.Fatalf("recent session did not produce evidence: %#v", report.RecentLogs)
	}
}

func TestAggregateExtractsExternalSystemsOnlyFromNetworkURLOperands(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	log := writeLogPlan(t,
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"rg 'https://search-fixture.example.com' ."}`},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"echo https://echo-fixture.example.com"}`},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"go test ./... -args https://test-fixture.example.com"}`},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"test https://shell-test-fixture.example.com = expected"}`},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"curl -H https://header-fixture.example.com --data https://payload-fixture.example.com https://api.example.com/status"}`},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"wget --quiet https://downloads.example.com/archive"}`},
	)

	report := aggregateLogsAt(t, now, []plan.PlanSummary{
		{ID: "url-operands", Dir: log, LastActivityAt: new(now)},
	})
	for _, name := range []string{"search-fixture.example.com", "echo-fixture.example.com", "test-fixture.example.com", "shell-test-fixture.example.com", "header-fixture.example.com", "payload-fixture.example.com"} {
		if findLogSignal(report.RecentLogs.ExternalSystems, name) != nil {
			t.Fatalf("non-network command URL %q produced external evidence: %#v", name, report.RecentLogs.ExternalSystems)
		}
	}
	for _, name := range []string{"api.example.com", "downloads.example.com"} {
		if findLogSignal(report.RecentLogs.ExternalSystems, name) == nil {
			t.Fatalf("network URL operand %q did not produce external evidence: %#v", name, report.RecentLogs.ExternalSystems)
		}
	}
}

func TestAggregateExtractsCommandSystemsOnlyAtUnquotedShellBoundaries(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	log := writeLogPlan(t,
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"echo 'fixture && gh issue list'"}`},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"printf 'fixture | kubectl get pods'"}`},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"test 'fixture && terraform plan' = expected"}`},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"gh issue list && kubectl get pods | terraform version"}`},
	)

	report := aggregateLogsAt(t, now, []plan.PlanSummary{
		{ID: "quoted-command-fixtures", Dir: log, LastActivityAt: new(now)},
	})
	for _, name := range []string{"gh", "kubectl", "terraform"} {
		tool := findLogSignal(report.RecentLogs.ToolUses, name)
		if tool == nil || tool.Count != 1 {
			t.Fatalf("command tool use %q = %#v in %#v", name, tool, report.RecentLogs.ToolUses)
		}
	}
	for _, name := range []string{"github.com", "kubernetes", "terraform"} {
		system := findLogSignal(report.RecentLogs.ExternalSystems, name)
		if system == nil || system.Count != 1 {
			t.Fatalf("command system %q = %#v in %#v", name, system, report.RecentLogs.ExternalSystems)
		}
	}
}

func TestAggregateRejectsCredentialShapedSignalNames(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	commandCredential := "ghp_1234567890abcdefghijkl"
	missingCredential := "sk-1234567890abcdefghijkl"
	log := writeLogPlan(t,
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"` + commandCredential + ` --version"}`},
		logrecord.Record{Type: logrecord.TypeToolResult, Name: "bash", Content: "bash: " + missingCredential + ": command not found"},
	)

	report := aggregateLogsAt(t, now, []plan.PlanSummary{
		{ID: "credentials", Dir: log, LastActivityAt: new(now)},
	})
	if findLogSignal(report.RecentLogs.ToolUses, commandCredential) != nil {
		t.Fatalf("credential-shaped command was retained: %#v", report.RecentLogs.ToolUses)
	}
	if findLogSignal(report.RecentLogs.MissingExecutables, missingCredential) != nil {
		t.Fatalf("credential-shaped missing executable was retained: %#v", report.RecentLogs.MissingExecutables)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{commandCredential, missingCredential} {
		if strings.Contains(string(encoded), credential) {
			t.Fatalf("serialized report leaked credential-shaped signal %q: %s", credential, encoded)
		}
	}
}

func TestAggregateIgnoresCopiedToolCallInMultilineAssistantText(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	log := writeLogPlan(t,
		logrecord.Record{Type: logrecord.TypeAssistant, Content: strings.Join([]string{
			"A copied transcript follows:",
			`→ bash {"command":"curl https://fabricated.example.com/status"}`,
			"✓ bash",
			"bash: fabricated-tool: command not found",
		}, "\n")},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"jq --version"}`},
		logrecord.Record{Type: logrecord.TypeToolResult, Name: "bash", Content: "bash: jq: command not found", Failed: true},
	)

	report := aggregateLogsAt(t, now, []plan.PlanSummary{
		{ID: "multiline-assistant", Dir: log, LastActivityAt: new(now)},
	})
	if findLogSignal(report.RecentLogs.ToolUses, "curl") != nil || findLogSignal(report.RecentLogs.ExternalSystems, "fabricated.example.com") != nil {
		t.Fatalf("assistant payload produced tool evidence: %#v", report.RecentLogs)
	}
	if findLogSignal(report.RecentLogs.MissingExecutables, "fabricated-tool") != nil {
		t.Fatalf("assistant payload produced missing executable evidence: %#v", report.RecentLogs.MissingExecutables)
	}
	if findLogSignal(report.RecentLogs.ToolUses, "jq") == nil || findLogSignal(report.RecentLogs.MissingExecutables, "jq") == nil {
		t.Fatalf("confirmed tool call was not recorded: %#v", report.RecentLogs)
	}
}

func TestAggregateIgnoresAdjacentCopiedCallAndResultInLegacyLog(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	log := writeRawLogPlan(t, strings.Join([]string{
		"assistant: copied transcript:",
		`→ bash {"command":"curl https://fabricated.example.com/status"}`,
		"✓ bash",
	}, "\n"))

	report := aggregateLogsAt(t, now, []plan.PlanSummary{
		{ID: "legacy-copy", Dir: log, LastActivityAt: new(now)},
	})
	if len(report.RecentLogs.ToolUses) != 0 || len(report.RecentLogs.ExternalSystems) != 0 {
		t.Fatalf("ambiguous legacy records produced evidence: %#v", report.RecentLogs)
	}
	if report.RecentLogs.Coverage.Scanned != 0 || report.RecentLogs.Coverage.Unsupported != 1 {
		t.Fatalf("legacy log coverage = %#v", report.RecentLogs.Coverage)
	}
}

func TestAggregateExcludesMixedLegacyAndFramedLog(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	dir := writeRawLogPlan(t, "--- 2026-07-01T12:00:00Z run ---\nassistant: legacy session\n")
	appendFramedLog(t, dir,
		logrecord.Record{Type: logrecord.TypeSession, Content: "recent", Timestamp: now.Format(time.RFC3339)},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"curl https://mixed.example.com/status"}`},
		logrecord.Record{Type: logrecord.TypeToolResult, Name: "bash", Content: "bash: mixed-tool: command not found"},
	)

	report := aggregateLogsAt(t, now, []plan.PlanSummary{{
		ID:             "mixed-log",
		Dir:            dir,
		LastActivityAt: new(now),
	}})
	if report.RecentLogs.Coverage.Eligible != 1 || report.RecentLogs.Coverage.Scanned != 0 || report.RecentLogs.Coverage.Unsupported != 1 {
		t.Fatalf("mixed log coverage = %#v", report.RecentLogs.Coverage)
	}
	if len(report.RecentLogs.MissingExecutables) != 0 || len(report.RecentLogs.ToolUses) != 0 || len(report.RecentLogs.ExternalSystems) != 0 {
		t.Fatalf("mixed log produced evidence: %#v", report.RecentLogs)
	}
}

func TestAggregateUnsupportedLogsDoNotExhaustByteBudget(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	summaries := make([]plan.PlanSummary, 0, maxTotalLogBytes/maxLogBytes+1)
	legacyLog := strings.Repeat("legacy output\n", maxLogBytes/len("legacy output\n"))
	for i := range maxTotalLogBytes / maxLogBytes {
		dir := writeRawLogPlan(t, legacyLog)
		summaries = append(summaries, plan.PlanSummary{
			ID:             fmt.Sprintf("legacy-%02d", i),
			Dir:            dir,
			LastActivityAt: new(now),
		})
	}
	valid := writeLogPlan(t, logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"jq --version"}`})
	summaries = append(summaries, plan.PlanSummary{ID: "valid", Dir: valid, LastActivityAt: new(now)})

	report := aggregateLogsAt(t, now, summaries)
	if report.RecentLogs.Coverage.Unsupported != maxTotalLogBytes/maxLogBytes || report.RecentLogs.Coverage.Scanned != 1 || report.RecentLogs.Coverage.WorkLimited != 0 {
		t.Fatalf("coverage = %#v", report.RecentLogs.Coverage)
	}
	if findLogSignal(report.RecentLogs.ToolUses, "jq") == nil {
		t.Fatalf("valid log was not scanned: %#v", report.RecentLogs)
	}
}

func TestAggregateIgnoresCopiedToolCallInShellOutput(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	log := writeLogPlan(t,
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"cat captured.log"}`},
		logrecord.Record{Type: logrecord.TypeToolResult, Name: "bash", Content: strings.Join([]string{
			`→ bash {"command":"curl https://fabricated.example.com/status"}`,
			"✓ bash",
			"bash: fabricated-tool: command not found",
		}, "\n")},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"jq --version"}`},
		logrecord.Record{Type: logrecord.TypeToolResult, Name: "bash", Content: "bash: jq: command not found", Failed: true},
	)

	report := aggregateLogsAt(t, now, []plan.PlanSummary{
		{ID: "shell-output", Dir: log, LastActivityAt: new(now)},
	})
	if findLogSignal(report.RecentLogs.ToolUses, "curl") != nil || findLogSignal(report.RecentLogs.ExternalSystems, "fabricated.example.com") != nil {
		t.Fatalf("shell output produced tool evidence: %#v", report.RecentLogs)
	}
	if findLogSignal(report.RecentLogs.MissingExecutables, "fabricated-tool") != nil {
		t.Fatalf("shell output produced missing executable evidence: %#v", report.RecentLogs.MissingExecutables)
	}
	if findLogSignal(report.RecentLogs.ToolUses, "cat") == nil || findLogSignal(report.RecentLogs.ToolUses, "jq") == nil || findLogSignal(report.RecentLogs.MissingExecutables, "jq") == nil {
		t.Fatalf("real tool calls were not recorded: %#v", report.RecentLogs)
	}
}

func TestAggregateIgnoresMissingExecutableTextInReadOutput(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	log := writeLogPlan(t,
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "read", Payload: `{"path":"example.go"}`},
		logrecord.Record{Type: logrecord.TypeToolResult, Name: "read", Content: "package example\n" + `const diagnostic = "jq: command not found"`},
	)

	report := aggregateLogsAt(t, now, []plan.PlanSummary{
		{ID: "read-output", Dir: log, LastActivityAt: new(now)},
	})
	if findLogSignal(report.RecentLogs.ToolUses, "read") == nil {
		t.Fatalf("read tool use not recorded: %#v", report.RecentLogs.ToolUses)
	}
	if findLogSignal(report.RecentLogs.MissingExecutables, "jq") != nil {
		t.Fatalf("read output produced missing executable evidence: %#v", report.RecentLogs.MissingExecutables)
	}
}

func TestAggregateIgnoresCopiedMissingExecutableTextInSearchOutput(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	log := writeLogPlan(t,
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"rg -n 'command not found' README.md"}`},
		logrecord.Record{Type: logrecord.TypeToolResult, Name: "bash", Content: "README.md:42: jq: command not found"},
	)

	report := aggregateLogsAt(t, now, []plan.PlanSummary{
		{ID: "search-output", Dir: log, LastActivityAt: new(now)},
	})
	if findLogSignal(report.RecentLogs.ToolUses, "rg") == nil {
		t.Fatalf("rg tool use not recorded: %#v", report.RecentLogs.ToolUses)
	}
	if findLogSignal(report.RecentLogs.MissingExecutables, "jq") != nil {
		t.Fatalf("search output produced missing executable evidence: %#v", report.RecentLogs.MissingExecutables)
	}
}

func TestAggregateCorrelatesMissingExecutableWithFailedCommand(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		command string
		failed  bool
		want    bool
	}{
		{name: "failed direct invocation", command: "jq --version", failed: true, want: true},
		{name: "successful copied file output", command: "cat errors.txt", failed: false},
		{name: "failed test output", command: "go test ./...", failed: true},
		{name: "ambiguous nested shell", command: "bash -c 'jq --version'", failed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := writeLogPlan(t,
				logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":` + fmt.Sprintf("%q", tt.command) + `}`},
				logrecord.Record{Type: logrecord.TypeToolResult, Name: "bash", Content: "jq: command not found", Failed: tt.failed},
			)
			report := aggregateLogsAt(t, now, []plan.PlanSummary{{ID: "correlation", Dir: log, LastActivityAt: new(now)}})
			got := findLogSignal(report.RecentLogs.MissingExecutables, "jq") != nil
			if got != tt.want {
				t.Fatalf("missing executable present = %t, want %t: %#v", got, tt.want, report.RecentLogs.MissingExecutables)
			}
		})
	}
}

func TestAggregateScansRecentLogWhenEventsAreDamaged(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	dir := writeLogPlan(t, logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"jq --version"}`})
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(strings.Repeat("x", 1024*1024+1)), 0o600); err != nil {
		t.Fatal(err)
	}

	report := aggregateLogsAt(t, now, []plan.PlanSummary{{
		ID:             "damaged-events",
		Dir:            dir,
		LastActivityAt: new(now),
	}})
	if report.PlansScanned != 0 || report.PlansSkipped != 1 {
		t.Fatalf("plan counts = scanned %d skipped %d", report.PlansScanned, report.PlansSkipped)
	}
	if report.RecentLogs.Coverage.Eligible != 1 || report.RecentLogs.Coverage.Scanned != 1 {
		t.Fatalf("recent log coverage = %#v", report.RecentLogs.Coverage)
	}
	if signal := findLogSignal(report.RecentLogs.ToolUses, "jq"); signal == nil || signal.Count != 1 {
		t.Fatalf("jq signal = %#v in %#v", signal, report.RecentLogs.ToolUses)
	}
}

func TestAggregateBoundsAndSkipsUnusableRecentLogs(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	old := writeLogPlan(t, logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"old-tool"}`})
	oversized := writeRawLogPlan(t, strings.Repeat("x", maxLogLineBytes+1)+" secret-oversized")
	malformed := writeRawLogPlan(t, "→ bash {not-json}\nmissing-tool: command not found\n")
	missing := t.TempDir()
	unreadable := t.TempDir()
	if err := os.Mkdir(filepath.Join(unreadable, "agent-run.log"), 0o700); err != nil {
		t.Fatal(err)
	}

	report := aggregateLogsAt(t, now, []plan.PlanSummary{
		{ID: "old", Dir: old, LastActivityAt: new(now.Add(-31 * 24 * time.Hour))},
		{ID: "no-recency", Dir: old},
		{ID: "oversized", Dir: oversized, LastActivityAt: new(now)},
		{ID: "malformed", Dir: malformed, LastActivityAt: new(now)},
		{ID: "missing", Dir: missing, LastActivityAt: new(now)},
		{ID: "unreadable", Dir: unreadable, LastActivityAt: new(now)},
	})
	coverage := report.RecentLogs.Coverage
	if coverage.MissingRecency != 1 || coverage.OutsideWindow != 1 || coverage.Eligible != 4 || coverage.Scanned != 0 || coverage.Missing != 1 || coverage.Unreadable != 1 || coverage.Unsupported != 1 || coverage.Oversized != 1 {
		t.Fatalf("coverage = %#v", coverage)
	}
	if len(report.RecentLogs.MissingExecutables) != 0 || len(report.RecentLogs.ToolUses) != 0 || len(report.RecentLogs.ExternalSystems) != 0 {
		t.Fatalf("unusable logs produced signals: %#v", report.RecentLogs)
	}
	encoded, err := json.Marshal(report.RecentLogs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-oversized") {
		t.Fatalf("oversized content leaked: %s", encoded)
	}
}

func TestSanitizeExcerptCapsAndRedactsCredentialForms(t *testing.T) {
	got := sanitizeExcerpt("  Authorization: Bearer abc123   password=hunter2 --token ghp_1234567890abcdefghijkl " + strings.Repeat("x", 300))
	if strings.Contains(got, "abc123") || strings.Contains(got, "hunter2") || strings.Contains(got, "ghp_1234567890abcdefghijkl") {
		t.Fatalf("credentials leaked: %q", got)
	}
	if len(got) != maxLogExcerptBytes || !strings.HasSuffix(got, "...") {
		t.Fatalf("excerpt was not capped: len=%d %q", len(got), got)
	}
}

func aggregateLogsAt(t *testing.T, now time.Time, summaries []plan.PlanSummary) Report {
	t.Helper()
	report := Report{}
	acc := accumulator{buckets: make(map[string]*ReasonBucket), logs: newLogAccumulator()}
	if err := aggregateSource(context.Background(), &report, &acc, sourceIdentity{id: "repo-a", name: "Alpha"}, fixtureLister{summaries: summaries}, now); err != nil {
		t.Fatal(err)
	}
	finalize(&report, &acc)
	return report
}

func writeLogPlan(t *testing.T, records ...logrecord.Record) string {
	t.Helper()
	return writeFramedLogPlan(t, append([]logrecord.Record{{Type: logrecord.TypeSession, Content: "test", Timestamp: "2026-07-29T12:00:00Z"}}, records...)...)
}

func writeFramedLogPlan(t *testing.T, records ...logrecord.Record) string {
	t.Helper()
	dir := t.TempDir()
	file, err := os.Create(filepath.Join(dir, "agent-run.log")) // #nosec G304 -- test temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if err := logrecord.Write(file, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeRawLogPlan(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent-run.log"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func appendFramedLog(t *testing.T, dir string, records ...logrecord.Record) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(dir, "agent-run.log"), os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- test temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if err := logrecord.Write(file, record); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

type streamFixtureProcess struct {
	stdout io.Reader
}

func (p *streamFixtureProcess) Stdin() io.WriteCloser { return nopWriteCloser{Writer: io.Discard} }
func (p *streamFixtureProcess) Stdout() io.Reader     { return p.stdout }
func (p *streamFixtureProcess) Stderr() io.Reader     { return strings.NewReader("") }
func (p *streamFixtureProcess) Wait() error           { return nil }
func (p *streamFixtureProcess) Kill() error           { return nil }

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

//go:fix inline
func findLogSignal(signals []LogSignal, name string) *LogSignal {
	for i := range signals {
		if signals[i].Name == name {
			return &signals[i]
		}
	}
	return nil
}

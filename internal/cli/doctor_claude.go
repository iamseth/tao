package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/theme"
)

const claudeProbeLimit = 32 * 1024

var claudeDoctorFlags = []string{"--print", "--output-format", "--verbose", "--no-session-persistence", "--permission-mode", "--model", "--effort"}
var claudeDoctorModes = []string{"auto", "plan", "bypassPermissions"}
var claudeVersionPattern = regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+) \(Claude Code\)$`)
var claudeOptionPattern = regexp.MustCompile(`^--[a-z][a-z-]*$`)
var claudeWordPattern = regexp.MustCompile(`[A-Za-z0-9_-]+`)
var claudeChoicesPattern = regexp.MustCompile(`\(choices:\s*([^)]*)\)`)

type claudeReadinessStage struct{ name, status, reason string }
type claudeReadiness struct {
	version string
	stages  []claudeReadinessStage
}

// No version currently has audited offline auth-status semantics. Advertising
// --json does not prove that status avoids network access or credential helpers.
// Keep this policy closed until version-specific evidence is recorded.
func claudeOfflineStatusKnown(_ string) bool { return false }

var probeDoctorClaudeReadiness = collectClaudeReadiness

func collectClaudeReadiness() *claudeReadiness {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	path, err := exec.LookPath("claude")
	if err != nil {
		path = ""
	}
	return probeClaudeReadiness(ctx, path, claudeOfflineStatusKnown)
}

func probeClaudeReadiness(ctx context.Context, path string, offlineStatusKnown func(string) bool) *claudeReadiness {
	r := &claudeReadiness{version: "version unknown", stages: []claudeReadinessStage{
		{"executable", "warning", "not found in PATH"},
		{"version", "unknown", "executable unavailable"},
		{"launch_flags", "unknown", "executable unavailable"},
		{"local_login", "unknown", "offline status semantics not established; not invoked"},
	}}
	if path == "" {
		return r
	}
	r.stages[0] = claudeReadinessStage{"executable", "ok", "found in PATH"}
	version, reason := runClaudePassive(ctx, path, "--version")
	if reason == "" {
		match := claudeVersionPattern.FindStringSubmatch(strings.TrimSpace(version))
		if len(match) == 2 && len(match[1]) <= 64 {
			r.version = match[1]
			r.stages[1] = claudeReadinessStage{"version", "ok", "measured with --version"}
		} else {
			reason = "empty or unrecognized version output"
		}
	}
	if reason != "" {
		r.stages[1].reason = reason
	}
	help, reason := runClaudePassive(ctx, path, "--help")
	if reason == "" {
		r.stages[2].status, r.stages[2].reason = claudeLaunchSurface(help)
	} else {
		r.stages[2].reason = reason
	}
	if offlineStatusKnown != nil && offlineStatusKnown(r.version) {
		output, failure := runClaudePassive(ctx, path, "auth", "status", "--json")
		r.stages[3].status, r.stages[3].reason = claudeLocalLogin(output, failure)
	}
	return r
}

type claudeBoundedOutput struct {
	data     []byte
	exceeded bool
}

func (b *claudeBoundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := claudeProbeLimit - len(b.data)
	if n > remaining {
		b.exceeded = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func runClaudePassive(ctx context.Context, path string, args ...string) (string, string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...) // #nosec G204 -- PATH-resolved Claude with fixed passive probe arguments, never shell input.
	// nil stdin is the null device; no prompt can be supplied.
	var output claudeBoundedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard // never expose vendor diagnostics or credential payloads
	cmd.WaitDelay = 100 * time.Millisecond
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", "probe canceled or timed out"
	}
	if output.exceeded {
		return "", "probe output exceeded limit"
	}
	if err != nil {
		return "", "probe failed"
	}
	return string(output.data), ""
}

// Parse option declarations, not mentions of flags elsewhere in help prose.
// Success means advertised surface only, not acceptance by a model session.
func claudeLaunchSurface(help string) (string, string) {
	sections := map[string]string{}
	var current []string
	for _, line := range strings.Split(help, "\n") {
		text := strings.TrimSpace(line)
		if strings.HasPrefix(text, "-") {
			current = nil
			// A help declaration's description begins after two spaces.
			declaration := strings.SplitN(text, "  ", 2)[0]
			for _, token := range strings.Fields(declaration) {
				token = strings.TrimSuffix(token, ",")
				if claudeOptionPattern.MatchString(token) {
					current = append(current, token)
				}
			}
		}
		for _, option := range current {
			sections[option] += " " + text
		}
	}
	if len(sections) == 0 {
		return "unknown", "empty or unrecognized help output"
	}
	for _, option := range claudeDoctorFlags {
		if _, ok := sections[option]; !ok {
			return "warning", "help does not advertise " + option
		}
	}
	for _, contract := range []struct {
		option string
		values []string
	}{{"--output-format", []string{"stream-json"}}, {"--permission-mode", claudeDoctorModes}} {
		option, values := contract.option, contract.values
		words := map[string]bool{}
		for _, choices := range claudeChoicesPattern.FindAllStringSubmatch(sections[option], -1) {
			for _, word := range claudeWordPattern.FindAllString(choices[1], -1) {
				words[word] = true
			}
		}
		for _, value := range values {
			if !words[value] {
				return "unknown", "help does not establish " + option + " value " + value
			}
		}
	}
	return "ok", "launch options and permission modes advertised; model/effort values are runtime-specific, not validated"
}

func claudeLocalLogin(output, failure string) (string, string) {
	if failure != "" {
		return "unknown", failure
	}
	var status struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	if decoder.Decode(&status) != nil || status.LoggedIn == nil {
		return "unknown", "malformed local status"
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return "unknown", "malformed local status"
	}
	if !*status.LoggedIn {
		return "warning", "locally logged out; remote validity not checked"
	}
	return "ok", "local login recorded; remote validity not checked"
}

func renderClaudeReadiness(out io.Writer, r *claudeReadiness, verbose bool, palette theme.Palette) error {
	if r == nil {
		return nil
	}
	shown := false
	for _, stage := range r.stages {
		if !verbose && stage.status == "ok" {
			continue
		}
		if !shown {
			if err := writeln(out, "\nClaude readiness (passive; no model request):"); err != nil {
				return err
			}
			shown = true
		}
		if err := writef(out, "  %s Claude %s: %s — %s\n", doctorStatusLabel(palette, stage.status, 9), r.version, stage.name, stage.reason); err != nil {
			return err
		}
	}
	return nil
}

package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	agentpkg "github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/testenv"
	"github.com/iamseth/tao/internal/theme"
)

const claudeTestHelp = "  --print  Print\n  --output-format <format>  (choices: text, json, stream-json)\n  --verbose  Verbose\n  --no-session-persistence  Disable\n  --permission-mode <mode>  (choices: auto, plan, bypassPermissions)\n  --model <model>  Model\n  --effort <level>  (low, medium, high)\n"

func TestClaudeLaunchSurface(t *testing.T) {
	for _, tc := range []struct{ name, help, status string }{
		{"supported", claudeTestHelp, "ok"},
		{"missing", strings.ReplaceAll(claudeTestHelp, "--verbose", "--other"), "warning"},
		{"substring flag", strings.ReplaceAll(claudeTestHelp, "--print", "--print-extra"), "warning"},
		{"punctuated flag", strings.ReplaceAll(claudeTestHelp, "--print", "--print_extra"), "warning"},
		{"value prose", strings.ReplaceAll(claudeTestHelp, "(choices: auto, plan, bypassPermissions)", "does not accept auto, plan, bypassPermissions"), "unknown"},
		{"substring value", strings.ReplaceAll(claudeTestHelp, "stream-json", "stream-json-extra"), "unknown"},
		{"missing mode", strings.ReplaceAll(claudeTestHelp, "bypassPermissions", "other"), "unknown"},
		{"empty", "", "unknown"},
		{"prose", "These flags include --print and --verbose", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status, _ := claudeLaunchSurface(tc.help); status != tc.status {
				t.Fatalf("got %s, want %s", status, tc.status)
			}
		})
	}
}

// The production constructor remains unchanged; this test pins every literal
// option it emits and the permission vocabulary it delegates to.
func TestClaudeDoctorClientContract(t *testing.T) {
	source, err := os.ReadFile("../agent/claude/client.go")
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`"(--[a-z-]+)"`).FindAllStringSubmatch(string(source), -1)
	var flags []string
	for _, match := range matches {
		flags = append(flags, match[1])
	}
	want := slices.Clone(claudeDoctorFlags)
	slices.Sort(flags)
	slices.Sort(want)
	if !reflect.DeepEqual(flags, want) {
		t.Fatalf("client flags %v != doctor %v", flags, want)
	}
	if !strings.Contains(string(source), `"--output-format", "stream-json"`) {
		t.Fatal("output format contract changed")
	}
	source, err = os.ReadFile("../agent/perm/perm.go")
	if err != nil {
		t.Fatal(err)
	}
	matches = regexp.MustCompile(`PermissionMode = "([^"]+)"`).FindAllStringSubmatch(string(source), -1)
	var modes []string
	for _, match := range matches {
		modes = append(modes, match[1])
	}
	want = slices.Clone(claudeDoctorModes)
	slices.Sort(modes)
	slices.Sort(want)
	if !reflect.DeepEqual(modes, want) {
		t.Fatalf("permission modes %v != doctor %v", modes, want)
	}
}

func claudeFake(t *testing.T, version, help, status, behavior string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path, log := filepath.Join(dir, "claude"), filepath.Join(dir, "argv")
	for name, data := range map[string]string{"version": version, "help": help, "status": status} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
if read -r input; then exit 91; fi
case "$*" in
 --version) file=version ;;
 --help) file=help ;;
 'auth status --json') file=status ;;
 *) exit 92 ;;
esac
%s
/bin/cat %q/"$file"
`, log, behavior, dir)
	if err := os.WriteFile(path, []byte(script), 0700); err != nil { // #nosec G306 -- executable fake in a private test directory.
		t.Fatal(err)
	}
	return path, log
}

func TestClaudePassiveProbe(t *testing.T) {
	testenv.SkipIfShort(t)
	for _, tc := range []struct{ name, version, help, behavior, vstatus, fstatus string }{
		{"supported", "2.1.308 (Claude Code)", claudeTestHelp, "", "ok", "ok"},
		{"missing flag", "2.1.308 (Claude Code)", strings.ReplaceAll(claudeTestHelp, "--verbose", "--other"), "", "ok", "warning"},
		{"empty", "", "", "", "unknown", "unknown"},
		{"failed", "", "", "exit 1", "unknown", "unknown"},
		{"bounded", strings.Repeat("x", claudeProbeLimit+1), strings.Repeat("x", claudeProbeLimit+1), "", "unknown", "unknown"},
		{"unsafe version", "2.1.308\x1b[31m secret", claudeTestHelp, "", "unknown", "ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, log := claudeFake(t, tc.version, tc.help, `{"loggedIn":true}`, tc.behavior)
			r := probeClaudeReadiness(context.Background(), path, claudeOfflineStatusKnown)
			if r.stages[1].status != tc.vstatus || r.stages[2].status != tc.fstatus || r.stages[3].status != "unknown" {
				t.Fatalf("%+v", r)
			}
			argv, err := os.ReadFile(log) // #nosec G304 -- argv record in this test's private directory.
			if err != nil {
				t.Fatal(err)
			}
			if string(argv) != "--version\n--help\n" {
				t.Fatalf("unexpected invocations %q", argv)
			}
			var out bytes.Buffer
			if err := renderClaudeReadiness(&out, r, true, theme.Palette{}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Claude "+r.version+":") || strings.Contains(out.String(), "secret") {
				t.Fatalf("unsafe/unattributed output: %s", &out)
			}
		})
	}
	r := probeClaudeReadiness(context.Background(), "", nil)
	if r.stages[0].status != "warning" || r.version != "version unknown" {
		t.Fatalf("%+v", r)
	}
}

func TestClaudeProbeCancellation(t *testing.T) {
	testenv.SkipIfShort(t)
	path, _ := claudeFake(t, "", "", "", "/bin/sleep 5")
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		if canceled {
			cancel()
		}
		start := time.Now()
		r := probeClaudeReadiness(ctx, path, nil)
		cancel()
		if time.Since(start) > time.Second || r.stages[1].reason != "probe canceled or timed out" {
			t.Fatalf("unbounded probe: %+v", r)
		}
	}
}

func TestClaudeLocalLogin(t *testing.T) {
	testenv.SkipIfShort(t)
	// A hypothetical audited version exercises the conditional path. Production
	// admits no versions: help alone is not evidence of offline semantics.
	for _, tc := range []struct{ output, behavior, status, reason string }{
		{`{"loggedIn":false,"token":"SECRET"}`, "", "warning", "locally logged out"},
		{`{"loggedIn":true,"token":"SECRET"}`, "", "ok", "local login recorded"},
		{`{"loggedIn":"true"}`, "", "unknown", "malformed"},
		{`{}`, "", "unknown", "malformed"},
		{`{"loggedIn":true} {}`, "", "unknown", "malformed"},
		{``, `if [ "$file" = status ]; then exit 2; fi`, "unknown", "probe failed"},
	} {
		path, log := claudeFake(t, "2.1.308 (Claude Code)", claudeTestHelp, tc.output, tc.behavior)
		r := probeClaudeReadiness(context.Background(), path, func(version string) bool { return version == "2.1.308" })
		stage := r.stages[3]
		if stage.status != tc.status || !strings.Contains(stage.reason, tc.reason) || strings.Contains(stage.reason, "SECRET") {
			t.Fatalf("%+v", stage)
		}
		argv, err := os.ReadFile(log) // #nosec G304 -- argv record in this test's private directory.
		if err != nil {
			t.Fatal(err)
		}
		if string(argv) != "--version\n--help\nauth status --json\n" {
			t.Fatalf("unexpected argv: %q", argv)
		}
	}
	for _, failure := range []string{"probe failed", "probe canceled or timed out", "unsupported status"} {
		if status, reason := claudeLocalLogin("", failure); status != "unknown" || reason != failure {
			t.Fatalf("%s %s", status, reason)
		}
	}
}

func TestDoctorClaudeGolden(t *testing.T) {
	var out bytes.Buffer
	for _, verbose := range []bool{false, true} {
		fmt.Fprintf(&out, "verbose=%t\n", verbose)
		report := doctorReport{selectedAgent: "claude", reviewAgent: "pi", agents: []agentpkg.Descriptor{{Kind: "claude", Label: "claude"}}, piReadiness: []doctorReadinessResult{{capability: "executable", status: "warning"}}, claudeReadiness: &claudeReadiness{version: "2.1.308", stages: []claudeReadinessStage{
			{"executable", "ok", "found in PATH"}, {"version", "ok", "measured with --version"},
			{"launch_flags", "warning", "help does not advertise --verbose"},
			{"local_login", "unknown", "offline status semantics not established; not invoked"},
		}}}
		render := renderCompactDoctor
		if verbose {
			render = renderVerboseDoctor
		}
		if err := render(&out, report, theme.Palette{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := renderClaudeReadiness(&out, probeClaudeReadiness(context.Background(), "", nil), false, theme.Palette{}); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "testdata/doctor_claude.golden", out.Bytes())
}

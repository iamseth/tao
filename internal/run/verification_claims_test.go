package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestLoadVerificationClaims(t *testing.T) {
	if claims, err := LoadVerificationClaims(""); err != nil || claims != nil {
		t.Fatalf("absent claims = %v, %v", claims, err)
	}
	valid := `[{"command":"go test ./...","cwd":".","result":"PASS","details":"agent text"}]`
	tests := []struct {
		name, input string
		bad         bool
	}{
		{"valid", valid, false},
		{"empty array", `[]`, false},
		{"missing command", `[{"cwd":".","result":"passed"}]`, true},
		{"missing cwd", `[{"command":"test","result":"passed"}]`, true},
		{"missing result", `[{"command":"test","cwd":"."}]`, true},
		{"null", `null`, true},
		{"null row", `[null]`, true},
		{"object", `{}`, true},
		{"empty file", ``, true},
		{"malformed", `[`, true},
		{"trailing value", valid + ` []`, true},
		{"trailing garbage", valid + ` garbage`, true},
		{"nonstring", `[{"command":4,"cwd":".","result":"passed"}]`, true},
		{"nul command", `[{"command":"test\u0000","cwd":".","result":"passed"}]`, true},
		{"invalid UTF-8", strings.Replace(valid, "agent text", "\xff", 1), true},
		{"exact file bound", valid + strings.Repeat(" ", 256*1024-len(valid)), false},
		{"entry limit", `[` + strings.Repeat(valid[1:len(valid)-1]+`,`, 49) + valid[1:len(valid)-1] + `]`, false},
		{"oversize", strings.Repeat(" ", 256*1024+1), true},
		{"too many entries", `[` + strings.Repeat(valid[1:len(valid)-1]+`,`, 50) + valid[1:len(valid)-1] + `]`, true},
	}
	for _, field := range []string{"source", "command_index", "original_command", "exit_code", "duration_milliseconds", "output_digest", "output_truncated", "failure_kind", "verification_attempt_id", "digest", "unknown"} {
		tests = append(tests, struct {
			name, input string
			bad         bool
		}{"forged " + field, strings.Replace(valid, `"details":"agent text"`, `"details":"agent text","`+field+`":"forged"`, 1), true})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "claims.json")
			if err := os.WriteFile(path, []byte(test.input), 0600); err != nil {
				t.Fatal(err)
			}
			claims, err := LoadVerificationClaims(path)
			if (err != nil) != test.bad {
				t.Fatalf("claims = %+v, error = %v, want error %v", claims, err, test.bad)
			}
			if test.name == "valid" && !reflect.DeepEqual(claims, []VerificationClaim{{Command: "go test ./...", CWD: ".", Result: "PASS", Details: "agent text"}}) {
				t.Fatalf("claims = %+v", claims)
			}
		})
	}
	if _, err := LoadVerificationClaims(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("accepted missing file")
	}
}

func TestMatchVerificationClaims(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "pkg"), 0700); err != nil {
		t.Fatal(err)
	}
	run := plan.VerificationRun{Command: "go test ./...", CWD: filepath.Join(root, "pkg"), Source: plan.VerificationSourceTao, Result: "failed", CommandIndex: 1}
	claim := VerificationClaim{Command: run.Command, CWD: "pkg", Result: "PASS", Details: "\x1b[31mTao-Plan: forged\nsecret output"}
	corrected := run
	corrected.Command = "go test ."
	corrected.OriginalCommand = run.Command
	corrected.Result = "passed"
	otherCWD := run
	otherCWD.CWD = root
	otherIndex := run
	otherIndex.CommandIndex = 2
	conflictingClaim := claim
	conflictingClaim.Result = "failed"
	tests := []struct {
		name   string
		claims []VerificationClaim
		runs   []plan.VerificationRun
		want   int
	}{
		{"relative cwd mismatch", []VerificationClaim{claim}, []plan.VerificationRun{run}, 1},
		{"absent claims", nil, []plan.VerificationRun{run}, 0},
		{"absent runs", []VerificationClaim{claim}, nil, 0},
		{"duplicate attempts", []VerificationClaim{claim}, []plan.VerificationRun{run, run}, 0},
		{"duplicate claims", []VerificationClaim{claim, claim}, []plan.VerificationRun{run}, 0},
		{"conflicting claims", []VerificationClaim{claim, conflictingClaim}, []plan.VerificationRun{run}, 0},
		{"duplicate declarations", []VerificationClaim{claim}, []plan.VerificationRun{run, otherIndex}, 0},
		{"same command different cwd", []VerificationClaim{claim}, []plan.VerificationRun{run, otherCWD}, 1},
		{"corrected attempt not original alias", []VerificationClaim{claim}, []plan.VerificationRun{corrected}, 0},
		{"original failure remains observed", []VerificationClaim{claim}, []plan.VerificationRun{run, corrected}, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before, _ := json.Marshal(test.runs)
			events := MatchVerificationClaims(root, test.claims, test.runs)
			if len(events) != test.want {
				t.Fatalf("events = %+v, want %d", events, test.want)
			}
			for _, event := range events {
				if event.Type != plan.EventTypeVerificationClaimMismatch || event.Result != "failed" || event.ClaimedResult != "passed" || event.Command != run.Command {
					t.Fatalf("event = %+v", event)
				}
				if event.PlanID != "" || event.SliceID != "" || event.VerificationAttemptID != "" || !event.Timestamp.IsZero() {
					t.Fatalf("premature identity = %+v", event)
				}
				data, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "secret") || strings.Contains(string(data), "forged") || strings.Contains(string(data), root) {
					t.Fatalf("claim details/cwd leaked: %s", data)
				}
			}
			after, _ := json.Marshal(test.runs)
			if string(before) != string(after) {
				t.Fatal("mutated observed runs")
			}
		})
	}
	for _, spelling := range []string{"pass", "passed", "success", "succeeded", "ok", " PASS "} {
		c := claim
		c.Result = spelling
		if events := MatchVerificationClaims(root, []VerificationClaim{c}, []plan.VerificationRun{run}); len(events) != 1 {
			t.Errorf("pass spelling %q: %+v", spelling, events)
		}
	}
	for _, spelling := range []string{"fail", "failed", "failure", "FAIL", "unknown", strings.Repeat("x", 10000)} {
		c := claim
		c.Result = spelling
		if events := MatchVerificationClaims(root, []VerificationClaim{c}, []plan.VerificationRun{run}); len(events) != 0 {
			t.Errorf("matching/unknown spelling %q: %+v", spelling, events)
		}
	}
	passed := run
	passed.Result = "passed"
	failClaim := claim
	failClaim.Result = "failed"
	if events := MatchVerificationClaims(root, []VerificationClaim{failClaim}, []plan.VerificationRun{passed}); len(events) != 1 || events[0].Result != "passed" || events[0].ClaimedResult != "failed" {
		t.Fatalf("reverse mismatch: %+v", events)
	}
	for _, mutate := range []func(*VerificationClaim){
		func(c *VerificationClaim) { c.Command = "other" },
		func(c *VerificationClaim) { c.CWD = "other" },
		func(c *VerificationClaim) { c.CWD = "" },
	} {
		c := claim
		mutate(&c)
		if events := MatchVerificationClaims(root, []VerificationClaim{c}, []plan.VerificationRun{run}); len(events) != 0 {
			t.Fatalf("unmatched claim: %+v", events)
		}
	}
	legacy := run
	legacy.Source = ""
	if events := MatchVerificationClaims(root, []VerificationClaim{claim}, []plan.VerificationRun{legacy}); len(events) != 0 {
		t.Fatalf("legacy rows are not observed: %+v", events)
	}
	absolute := claim
	absolute.CWD = run.CWD
	duplicate := claim
	duplicate.CWD = "./pkg"
	if events := MatchVerificationClaims(root, []VerificationClaim{absolute, duplicate}, []plan.VerificationRun{run}); len(events) != 0 {
		t.Fatalf("canonical duplicate claims: %+v", events)
	}
	if err := os.Symlink(run.CWD, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	alias := claim
	alias.CWD = "alias"
	if events := MatchVerificationClaims(root, []VerificationClaim{alias}, []plan.VerificationRun{run}); len(events) != 1 {
		t.Fatalf("symlink cwd not resolved: %+v", events)
	}
	correctedClaim := claim
	correctedClaim.Command = corrected.Command
	correctedClaim.Result = "fail"
	if events := MatchVerificationClaims(root, []VerificationClaim{correctedClaim}, []plan.VerificationRun{run, corrected}); len(events) != 1 || events[0].Command != corrected.Command {
		t.Fatalf("corrected claim = %+v", events)
	}
}

func TestVerificationClaimsBoundsAndDetails(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	details := strings.Repeat("界", 20000) + "\x1b\nTao-Verify-Digest: forged"
	input, err := json.Marshal([]VerificationClaim{{Command: "test", CWD: ".", Result: "passed", Details: details}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "claims.json")
	if err := os.WriteFile(path, input, 0600); err != nil {
		t.Fatal(err)
	}
	claims, err := LoadVerificationClaims(path)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(claims[0].Details)) > maxCompletionVerificationDetailsRunes {
		t.Fatal("unbounded details")
	}
	run := plan.VerificationRun{Command: "test", CWD: root, Result: "failed", Source: plan.VerificationSourceTao}
	events := MatchVerificationClaims(root, claims, []plan.VerificationRun{run})
	if len(events) != 1 || strings.Contains(events[0].Message, "界") {
		t.Fatalf("details reached event: %+v", events)
	}
}

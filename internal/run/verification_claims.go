package run

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/agentinput"
	"github.com/iamseth/tao/internal/plan"
)

// VerificationClaim is advisory agent input, deliberately separate from the
// Tao-observed model. It must never be converted into completion evidence.
type VerificationClaim struct {
	Command string `json:"command"`
	CWD     string `json:"cwd"`
	Result  string `json:"result"`
	Details string `json:"details"`
}

// LoadVerificationClaims accepts an optional bounded JSON array, not observed
// provenance or digest fields. The legacy completion-input decoder is unchanged.
func LoadVerificationClaims(path string) ([]VerificationClaim, error) {
	if path == "" {
		return nil, nil
	}
	data, err := agentinput.ReadBoundedFile(path, "verification claims file", maxCompletionVerificationResultsBytes)
	if err != nil {
		return nil, fmt.Errorf("read verification claims: %w", err)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("verification claims must be UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var claims []VerificationClaim
	if err := decoder.Decode(&claims); err != nil {
		return nil, fmt.Errorf("read verification claims JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("verification claims must contain exactly one JSON array")
	}
	if claims == nil {
		return nil, fmt.Errorf("verification claims must be an array")
	}
	if len(claims) > maxCompletionVerificationResults {
		return nil, fmt.Errorf("verification claims exceed %d entries", maxCompletionVerificationResults)
	}
	for i := range claims {
		claim := &claims[i]
		if !validSliceVerificationText(claim.Command) || !validSliceVerificationText(claim.CWD) || !validSliceVerificationText(claim.Result) || len(claim.Result) > plan.MaxVerificationIdentityBytes {
			return nil, fmt.Errorf("verification claim %d requires bounded command, cwd, and result text", i+1)
		}
		claim.Details = agentinput.CapRunes(claim.Details, maxCompletionVerificationDetailsRunes)
	}
	return claims, nil
}

type verificationClaimKey struct{ command, cwd string }

// MatchVerificationClaims emits diagnostics only for one-to-one command/cwd
// matches. Repeated claims or observed commands are ambiguous, even if results
// agree. Corrected attempts match their executed command, never OriginalCommand.
// Claim details are never projected. The transaction supplies event identity.
func MatchVerificationClaims(root string, claims []VerificationClaim, runs []plan.VerificationRun) []plan.Event {
	if len(claims) == 0 || len(claims) > maxCompletionVerificationResults || len(runs) > plan.MaxSliceVerificationRuns {
		return nil
	}
	root, err := canonicalVerificationDirectory(root)
	if err != nil {
		return nil
	}
	keys := make([]verificationClaimKey, len(claims))
	counts := make(map[verificationClaimKey]int)
	for i, claim := range claims {
		if !validSliceVerificationText(claim.Command) || !validSliceVerificationText(claim.CWD) {
			continue
		}
		cwd := claim.CWD
		if !filepath.IsAbs(cwd) {
			cwd = filepath.Join(root, cwd)
		}
		cwd, err = canonicalVerificationDirectory(cwd)
		if err != nil {
			continue
		}
		key := verificationClaimKey{claim.Command, cwd}
		keys[i] = key
		counts[key]++
	}
	observed := make(map[verificationClaimKey][]int)
	for i, run := range runs {
		key := verificationClaimKey{run.Command, run.CWD}
		observed[key] = append(observed[key], i)
	}
	var events []plan.Event
	for i, claim := range claims {
		key := keys[i]
		matches := observed[key]
		claimed := normalizeVerificationClaimResult(claim.Result)
		if key.command == "" || counts[key] != 1 || len(matches) != 1 || claimed == "" {
			continue
		}
		run := runs[matches[0]]
		if run.Source != plan.VerificationSourceTao || (run.Result != "passed" && run.Result != "failed") || run.Result == claimed {
			continue
		}
		events = append(events, plan.Event{
			Type:    plan.EventTypeVerificationClaimMismatch,
			Command: verificationPresentation(run.Command, plan.MaxVerificationCommandBytes),
			Result:  run.Result, ClaimedResult: claimed,
			Message: "Advisory verification claim differs from the Tao-observed result",
		})
	}
	return events
}

func normalizeVerificationClaimResult(result string) string {
	if len(result) > plan.MaxVerificationIdentityBytes {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(result)) {
	case "pass", "passed", "success", "succeeded", "ok":
		return "passed"
	case "fail", "failed", "failure":
		return "failed"
	default:
		return ""
	}
}

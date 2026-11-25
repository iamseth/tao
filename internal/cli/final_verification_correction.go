package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/iamseth/tao/internal/agentinput"
	"github.com/iamseth/tao/internal/plan"
	runimpl "github.com/iamseth/tao/internal/run"
)

const finalCorrectionUsage = "tao edit final-verification <plan> --proposal-file FILE [--confirm TOKEN]"

func registerFinalCorrectionFlags(fs *flag.FlagSet) {
	fs.String("proposal-file", "", "bounded JSON source-contract correction proposal")
	fs.String("confirm", "", "exact preview token authorizing this transition")
}

func (a App) editFinalVerification(ctx context.Context, repo editRepository, args []string) error {
	fs, positional, err := a.parseArgs("edit final-verification", args, registerFinalCorrectionFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 1, "usage: "+finalCorrectionUsage); err != nil {
		return err
	}
	request, err := readFinalCorrectionProposal(flagStringValue(fs, "proposal-file"))
	if err != nil {
		return err
	}
	request.At = a.now().UTC()
	request.Confirmation = flagStringValue(fs, "confirm")
	record, err := repo.ResolvePlanRecord(ctx, positional[0])
	if err != nil {
		return err
	}
	if request.Confirmation != "" {
		if err := runimpl.CorrectFinalVerification(ctx, record, request, a.CommandRunner); err != nil {
			return err
		}
		return writef(a.Out, "Final contract correction recorded. Fresh verification and review required; completed work and lifetime attempts preserved.\n")
	}
	detail, preview, err := runimpl.PreviewFinalVerificationCorrection(ctx, record, request, a.CommandRunner)
	if err != nil {
		return err
	}
	oldCommand, oldCWD := "", ""
	if c := detail.State.Plan.FinalVerificationContract; c != nil {
		oldCommand, oldCWD = c.Command, c.CWD
	} else if v := detail.State.Plan.FinalVerification; v != nil {
		oldCommand, oldCWD = v.Command, v.CWD
	}
	oldContract, err := json.Marshal(detail.State.Plan.FinalVerificationContract)
	if err != nil {
		return err
	}
	newContract, err := json.Marshal(request.Contract)
	if err != nil {
		return err
	}
	if err := writef(a.Out, "Old source contract: %q\nNew source contract: %q\n", string(oldContract), string(newContract)); err != nil {
		return err
	}
	return writef(a.Out, "Final verification correction preview (no gates executed)\nOld command: %q\nOld cwd: %q\nNew command: %q\nNew cwd: %q\nReason: %q\nREMOVED COVERAGE: %q\nDEFERRED CHECKS: %q\nSuperseded repairs: %q\nPrior verification and review/approval: invalidated\nRemaining lifetime repair attempts: %d\nConfirmation authorizes this transition only, not identity or code correctness. Reduced coverage is not build-equivalent.\nPreview token: %s\nConfirm with --confirm %s using the same proposal.\n", oldCommand, oldCWD, request.Contract.Command, request.Contract.CWD, request.Reason, request.CoverageRemoved, request.DeferredChecks, preview.SupersededSlices, max(0, plan.VerificationRepairAttemptCap-plan.VerificationRepairAttemptCount(detail)), preview.Token, preview.Token)
}

func readFinalCorrectionProposal(path string) (plan.FinalVerificationCorrectionRequest, error) {
	var request plan.FinalVerificationCorrectionRequest
	if path == "" {
		return request, fmt.Errorf("--proposal-file is required")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return request, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return request, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return request, err
	}
	if !info.Mode().IsRegular() {
		return request, fmt.Errorf("proposal must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, agentinput.MaxFileBytes+1))
	if err != nil {
		return request, err
	}
	if int64(len(data)) > agentinput.MaxFileBytes {
		return request, fmt.Errorf("proposal exceeds byte limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueCorrectionJSON(decoder, 0); err != nil {
		return request, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return request, fmt.Errorf("proposal requires exactly one JSON object")
	}
	var input struct {
		ExpectedDigest  string                         `json:"expected_digest"`
		ExpectedHead    string                         `json:"expected_head"`
		Contract        plan.FinalVerificationContract `json:"contract"`
		Reason          string                         `json:"reason"`
		CoverageRemoved []string                       `json:"coverage_removed"`
		DeferredChecks  []string                       `json:"deferred_checks"`
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return request, err
	}
	if strings.TrimSpace(input.Reason) == "" || input.CoverageRemoved == nil || input.DeferredChecks == nil || input.Contract.Coverage == nil {
		return request, fmt.Errorf("reason, contract coverage, coverage_removed and deferred_checks declarations are required (use [] for none)")
	}
	request.ExpectedDigest, request.ExpectedHead, request.Contract = input.ExpectedDigest, input.ExpectedHead, input.Contract
	request.Reason, request.CoverageRemoved, request.DeferredChecks = input.Reason, input.CoverageRemoved, input.DeferredChecks
	return request, nil
}

// Detect duplicate keys at every level before decoding into typed fields.
func uniqueCorrectionJSON(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return fmt.Errorf("proposal nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("invalid object key")
			}
			name = strings.ToLower(name)
			if seen[name] {
				return fmt.Errorf("duplicate proposal field %q", name)
			}
			seen[name] = true
			if err := uniqueCorrectionJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := uniqueCorrectionJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

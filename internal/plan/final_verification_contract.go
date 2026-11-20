package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/agentinput"
)

// RequireCurrentFinalVerification gates new authority on the durable source
// contract and exact workspace head. Legacy plans retain their existing rules.
func RequireCurrentFinalVerification(detail *PlanDetail) error {
	if detail == nil {
		return fmt.Errorf("final verification requires plan detail")
	}
	contract := detail.State.Plan.FinalVerificationContract
	if contract == nil {
		return nil
	}
	digest, err := contract.Digest()
	if err != nil {
		return fmt.Errorf("final verification required: %w", err)
	}
	v := detail.State.Plan.FinalVerification
	w := detail.State.Workspace
	if v == nil || v.Result != "passed" || v.ContractDigest != digest || w == nil || strings.TrimSpace(w.HeadSHA) == "" || v.HeadSHA != w.HeadSHA {
		return fmt.Errorf("passing final verification required for current contract and workspace head")
	}
	return nil
}

// requireCurrentFinalApproval protects new publication transactions, not frozen intents.
func requireCurrentFinalApproval(detail *PlanDetail, head string) error {
	if err := RequireCurrentFinalVerification(detail); err != nil {
		return err
	}
	if detail.State.Plan.FinalVerificationContract == nil {
		return nil
	}
	review := CurrentReview(detail)
	if review == nil || !review.IsApproved() || review.Head != head || head != detail.State.Workspace.HeadSHA {
		return fmt.Errorf("fresh approval required for current final contract and workspace head")
	}
	return nil
}

// FinalVerificationPrerequisite declares passive evidence, never a probe command.
// Only file and executable kinds are observable; other kinds remain unobservable.
type FinalVerificationPrerequisite struct {
	Kind string `json:"kind"`
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

// FinalVerificationObservation contains identities and digests, not file contents
// or environment values. An observation alone grants no execution authority.
type FinalVerificationObservation struct {
	Kind     string `json:"kind"`
	Identity string `json:"identity"`
	Digest   string `json:"digest"`
}

// FinalVerificationContract is an optional, revisioned source-gate declaration.
// Effects describe intent, not a sandbox. List order and shell text are semantic.
type FinalVerificationContract struct {
	Revision            int                             `json:"revision"`
	Command             string                          `json:"command"`
	CWD                 string                          `json:"cwd"`
	Purpose             string                          `json:"purpose"`
	Coverage            []string                        `json:"coverage"`
	Prerequisites       []FinalVerificationPrerequisite `json:"prerequisites,omitempty"`
	PermittedEffects    []string                        `json:"permitted_effects,omitempty"`
	ComparisonPermitted bool                            `json:"comparison_permitted"`
	Provenance          string                          `json:"provenance"`
}

// Validate bounds the declaration without executing or normalizing it.
func (c FinalVerificationContract) Validate() error {
	if c.Revision <= 0 {
		return fmt.Errorf("final verification contract revision must be positive")
	}
	for _, field := range []struct{ name, value string }{{"command", c.Command}, {"cwd", c.CWD}, {"purpose", c.Purpose}, {"provenance", c.Provenance}} {
		if err := contractText(field.value, field.name, true); err != nil {
			return err
		}
	}
	if !filepath.IsLocal(c.CWD) || strings.Contains(c.CWD, "\\") {
		return fmt.Errorf("final verification contract cwd must be relative and contained")
	}
	for _, part := range strings.Split(c.CWD, "/") {
		if part == ".." {
			return fmt.Errorf("final verification contract cwd must not traverse parents")
		}
	}
	if len(c.Coverage) == 0 {
		return fmt.Errorf("final verification contract coverage is required")
	}
	if len(c.Coverage) > 256 || len(c.Prerequisites) > 256 || len(c.PermittedEffects) > 256 {
		return fmt.Errorf("final verification contract lists exceed 256 entries")
	}
	for _, values := range [][]string{c.Coverage, c.PermittedEffects} {
		for _, value := range values {
			if err := contractText(value, "list entry", true); err != nil {
				return err
			}
		}
	}
	for _, p := range c.Prerequisites {
		if err := contractText(p.Kind, "prerequisite kind", true); err != nil {
			return err
		}
		if err := contractText(p.Path, "prerequisite path", p.Kind == "file"); err != nil {
			return err
		}
		if err := contractText(p.Name, "prerequisite name", p.Kind == "executable"); err != nil {
			return err
		}
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if int64(len(data)) > agentinput.MaxFileBytes {
		return fmt.Errorf("final verification contract exceeds %d bytes", agentinput.MaxFileBytes)
	}
	return nil
}

func contractText(value, label string, required bool) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("final verification contract %s contains invalid text", label)
	}
	if utf8.RuneCountInString(value) > agentinput.MaxTextRunes {
		return fmt.Errorf("final verification contract %s exceeds text bound", label)
	}
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("final verification contract %s is required", label)
	}
	return nil
}

// Digest hashes the exact validated semantic declaration using a versioned domain.
func (c FinalVerificationContract) Digest() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("tao.final-verification-contract.v1\n"), data...))
	return hex.EncodeToString(sum[:]), nil
}

// FinalVerificationComparisonRun retains each run's passive observations separately.
type FinalVerificationComparisonRun struct {
	HeadSHA      string                         `json:"head_sha"`
	CWD          string                         `json:"cwd"`
	Result       string                         `json:"result"`
	Observations []FinalVerificationObservation `json:"observations,omitempty"`
	OutputDigest string                         `json:"output_digest,omitempty"`
	ExitCode     *int                           `json:"exit_code,omitempty"`
}

// FinalVerificationComparison retains Tao-observed comparison evidence.
type FinalVerificationComparison struct {
	ContractDigest string                           `json:"contract_digest"`
	BaseSHA        string                           `json:"base_sha"`
	HeadSHA        string                           `json:"head_sha"`
	Runs           []FinalVerificationComparisonRun `json:"runs"`
}

// AutomaticVerificationRepairAdmissible requires an operational regression under
// one current contract. This is not proof against undeclared external causes.
// Only Tao's recording path produces this evidence; telemetry is not consulted.
func AutomaticVerificationRepairAdmissible(detail *PlanDetail) (bool, string) {
	refuse := func() (bool, string) {
		return false, "automatic repair requires a current permitted base-pass/two-head-fail comparison"
	}
	if detail == nil || !AnalyzeRunCapabilities(detail).Complete {
		return refuse()
	}
	failure := CurrentFailedFinalVerification(detail)
	c := detail.State.Plan.FinalVerificationContract
	if failure == nil || c == nil || failure.FailureKind != FinalVerificationFailureKindCode || !c.ComparisonPermitted || len(c.Prerequisites) == 0 || len(c.PermittedEffects) == 0 {
		return refuse()
	}
	if ok, reason := RepairVerificationAdmissible(detail); !ok {
		return false, reason
	}
	digest, err := c.Digest()
	comparison := failure.Comparison
	if err != nil || failure.ContractDigest != digest || failure.Command != c.Command || comparison == nil || comparison.ContractDigest != digest || comparison.HeadSHA != failure.HeadSHA || comparison.BaseSHA == "" || comparison.BaseSHA == comparison.HeadSHA || comparison.BaseSHA != detail.State.Repo.BaseCommit || len(comparison.Runs) != 3 {
		return refuse()
	}
	first, retry, base := comparison.Runs[0], comparison.Runs[1], comparison.Runs[2]
	if first.HeadSHA != comparison.HeadSHA || retry.HeadSHA != comparison.HeadSHA || base.HeadSHA != comparison.BaseSHA || first.CWD != c.CWD || retry.CWD != c.CWD || base.CWD != c.CWD || first.Result != "failed" || retry.Result != "failed" || base.Result != "passed" || first.ExitCode == nil || retry.ExitCode == nil || base.ExitCode == nil || *first.ExitCode <= 0 || *first.ExitCode == 126 || *first.ExitCode == 127 || *retry.ExitCode != *first.ExitCode || *base.ExitCode != 0 || !contractSHA256(first.OutputDigest) || first.OutputDigest != retry.OutputDigest || !contractSHA256(base.OutputDigest) {
		return refuse()
	}
	if len(first.Observations) != len(c.Prerequisites) || !slices.Equal(first.Observations, retry.Observations) || !slices.Equal(first.Observations, base.Observations) {
		return refuse()
	}
	for i, observation := range first.Observations {
		p := c.Prerequisites[i]
		if observation.Kind != p.Kind || !contractSHA256(observation.Digest) {
			return refuse()
		}
		switch p.Kind {
		case "file":
			if !filepath.IsLocal(p.Path) || observation.Identity != filepath.ToSlash(filepath.Clean(p.Path)) {
				return refuse()
			}
		case "executable":
			if !filepath.IsAbs(observation.Identity) || filepath.Base(observation.Identity) != p.Name {
				return refuse()
			}
		default:
			return refuse()
		}
	}
	return true, "observed operational regression under the current contract"
}

func contractSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

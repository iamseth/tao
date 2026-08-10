package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/atomicfile"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/planreport"
)

var reportCommand = commandMetadata{
	name:                  "report",
	usageLines:            []string{"report --output PATH [--planning-only] [--force] <plan-id-or-slug-or-path>"},
	completionDescription: "Export a share-safe Markdown plan report",
	long:                  "Export one readable Tao plan as a sanitized Markdown report. Output is required; use - to write a pure Markdown stream to stdout. File output is owner-only and is not replaced unless --force is supplied.",
	examples: "  tao report --output plan-report.md my-plan\n" +
		"  tao report --planning-only --output prompts/my-plan.md my-plan\n" +
		"  tao report --output - 20260628-1618-kubectl-style-help",
	registerFlags: registerReportFlags,
	completion: completionContext{
		flagValues: map[string]completionFlagValue{"output": {kind: completionValuePath, label: "path"}},
		positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs},
	},
	repository: repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.report(c.ctx, c.repo, c.args)
	},
}

func registerReportFlags(fs *flag.FlagSet) {
	fs.String("output", "", "output Markdown path, or - for stdout (required)")
	fs.Bool("planning-only", false, "export a synthesized planning record without execution history")
	fs.Bool("force", false, "safely replace an existing output file")
}

func (a App) report(ctx context.Context, repo plan.Resolver, args []string) error {
	fs, positional, err := a.parseArgs("report", args, registerReportFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 1, "usage: tao report --output PATH [--planning-only] [--force] <plan-id-or-slug-or-path>"); err != nil {
		return err
	}
	output := flagStringValue(fs, "output")
	if strings.TrimSpace(output) == "" {
		return errors.New("tao report requires --output PATH (use - for stdout)")
	}

	detail, err := repo.ResolvePlan(ctx, positional[0])
	if err != nil || detail == nil {
		return errors.New("report could not read the selected plan")
	}

	var document []byte
	if flagBoolValue(fs, "planning-only") {
		document, err = planreport.RenderPlanningOnly(planreport.ProjectPlanningOnly(detail, a.now()))
	} else {
		document, err = planreport.RenderFull(planreport.ProjectFull(detail, a.now()))
	}
	if err != nil {
		return errors.New("report could not safely render the selected plan")
	}
	if output == "-" {
		if _, err := a.Out.Write(document); err != nil {
			return errors.New("report could not write Markdown to stdout")
		}
		return nil
	}

	inside, err := reportPathInsidePlan(output, detail.Dir)
	if err != nil {
		return errors.New("report could not safely resolve the output destination")
	}
	if inside {
		return errors.New("report output must be outside the selected plan directory")
	}
	if err := atomicfile.Write(output, document, atomicfile.Options{Perm: 0o600, Exclusive: !flagBoolValue(fs, "force")}); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("report output already exists; use --force to replace it")
		}
		return errors.New("report could not install the output file")
	}
	return writef(a.Err, "Report written.\n")
}

func reportPathInsidePlan(output, planDir string) (bool, error) {
	if strings.TrimSpace(planDir) == "" {
		return false, errors.New("selected plan directory is unavailable")
	}
	canonicalPlan, err := filepath.EvalSymlinks(planDir)
	if err != nil {
		return false, fmt.Errorf("canonicalize plan directory: %w", err)
	}
	canonicalOutput, err := canonicalReportPath(output)
	if err != nil {
		return false, err
	}
	canonicalPlan, err = filepath.Abs(canonicalPlan)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(filepath.Clean(canonicalPlan), filepath.Clean(canonicalOutput))
	if err != nil {
		return false, err
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}

// canonicalReportPath resolves symlinks in the deepest existing ancestor while
// still supporting a new destination file.
func canonicalReportPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	candidate := filepath.Clean(absolute)
	var suffix []string
	for {
		resolved, resolveErr := filepath.EvalSymlinks(candidate)
		if resolveErr == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", resolveErr
		}
		suffix = append(suffix, filepath.Base(candidate))
		candidate = parent
	}
}

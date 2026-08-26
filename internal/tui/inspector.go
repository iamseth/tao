package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

const (
	detailInspectionMaxFindings = 12
	detailInspectionMaxText     = 240
)

// DetailFinding is one bounded, advisory observation shown on a plan overview.
type DetailFinding struct {
	Severity string
	Message  string
}

// DetailInspection is the read-only result of inspecting one loaded plan.
type DetailInspection struct {
	Findings []DetailFinding
}

// DetailInspector inspects a loaded plan without changing its lifecycle state.
type DetailInspector interface {
	Inspect(context.Context, *plan.PlanDetail) (DetailInspection, error)
}

// DetailInspectorFunc adapts a function to DetailInspector.
type DetailInspectorFunc func(context.Context, *plan.PlanDetail) (DetailInspection, error)

func (f DetailInspectorFunc) Inspect(ctx context.Context, detail *plan.PlanDetail) (DetailInspection, error) {
	return f(ctx, detail)
}

type detailInspectionStatus int

const (
	detailInspectionUnavailable detailInspectionStatus = iota
	detailInspectionLoading
	detailInspectionReady
	detailInspectionFailed
)

type detailInspectionView struct {
	status   detailInspectionStatus
	findings []DetailFinding
	err      string
}

type detailInspectionUpdate struct {
	key    string
	result DetailInspection
	err    error
}

func boundedDetailInspection(result DetailInspection) DetailInspection {
	bounded := DetailInspection{Findings: make([]DetailFinding, 0, min(len(result.Findings), detailInspectionMaxFindings))}
	for _, finding := range result.Findings {
		severity := truncatePlain(singleLineDetail(finding.Severity), 24)
		message := truncatePlain(singleLineDetail(finding.Message), detailInspectionMaxText)
		if message == "" {
			continue
		}
		if severity == "" {
			severity = "info"
		}
		bounded.Findings = append(bounded.Findings, DetailFinding{Severity: severity, Message: message})
		if len(bounded.Findings) == detailInspectionMaxFindings {
			break
		}
	}
	return bounded
}

// detailInspectionKey contains only inputs used by the production staleness
// inspection, so ordinary dashboard refreshes do not repeat unchanged Git work.
func detailInspectionKey(detail *plan.PlanDetail) string {
	if detail == nil {
		return ""
	}
	var value strings.Builder
	value.WriteString(detail.State.Plan.ID)
	value.WriteByte(0)
	value.WriteString(detail.State.Repo.Root)
	value.WriteByte(0)
	value.WriteString(detail.State.Repo.BaseCommit)
	for _, id := range detail.State.Plan.PendingSlices {
		value.WriteByte(0)
		value.WriteString(id)
	}
	for _, slice := range detail.Slices.Slices {
		value.WriteByte(0)
		value.WriteString(slice.ID)
		value.WriteByte(0)
		value.WriteString(slice.Status)
		for _, file := range slice.ExpectedFiles {
			value.WriteByte(0)
			value.WriteString(file)
		}
	}
	sum := sha256.Sum256([]byte(value.String()))
	return hex.EncodeToString(sum[:])
}

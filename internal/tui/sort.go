package tui

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/monitor/rowlabel"
	"github.com/iamseth/tao/internal/note"
)

// SortStore persists sort preferences independently of filters.
type SortStore interface {
	Load(context.Context) (PageSorts, error)
	Save(context.Context, PageSorts) error
}

type SortColumn string

const (
	SortDefault  SortColumn = "default"
	SortRepo     SortColumn = "repo"
	SortNext     SortColumn = "next"
	SortPlan     SortColumn = "plan"
	SortPriority SortColumn = "priority"
	SortSlices   SortColumn = "slices"
	SortRun      SortColumn = "run"
	SortAge      SortColumn = "age"
	SortPreview  SortColumn = "preview"
	SortTags     SortColumn = "tags"
	SortTier     SortColumn = "tier"
	SortCreated  SortColumn = "created"
	SortUpdated  SortColumn = "updated"
)

type Sort struct {
	Column     SortColumn
	Descending bool
}

func (s Sort) IsDefault() bool { return s.Column == "" || s.Column == SortDefault }

type PageSorts struct{ Plans, History, Notes Sort }

func (p PageSorts) For(page PageID) Sort {
	switch page {
	case PagePlans:
		return p.Plans
	case PageHistory:
		return p.History
	case PageNotes:
		return p.Notes
	}
	return Sort{Column: SortDefault}
}
func (p *PageSorts) Set(page PageID, sort Sort) {
	switch page {
	case PagePlans:
		p.Plans = sort
	case PageHistory:
		p.History = sort
	case PageNotes:
		p.Notes = sort
	}
}

type SortOption struct {
	Column            SortColumn
	Label             string
	DescendingDefault bool
}

func SortOptionsForPage(page PageID) []SortOption {
	options := []SortOption{{SortDefault, "Default", false}}
	switch page {
	case PagePlans, PageHistory:
		return append(options, SortOption{SortRepo, "REPO", false}, SortOption{SortNext, "NEXT", false}, SortOption{SortPlan, "PLAN", false}, SortOption{SortPriority, "I/R/E", false}, SortOption{SortSlices, "SLICES", true}, SortOption{SortRun, "RUN", true}, SortOption{SortAge, "AGE", true})
	case PageNotes:
		return append(options, SortOption{SortRepo, "REPO", false}, SortOption{SortPreview, "PREVIEW", false}, SortOption{SortTags, "TAGS", false}, SortOption{SortTier, "TIER", false}, SortOption{SortCreated, "CREATED", true}, SortOption{SortUpdated, "UPDATED", true})
	}
	return options
}
func NormalizeSort(page PageID, sort Sort) Sort {
	if !sort.IsDefault() {
		for _, option := range SortOptionsForPage(page) {
			if option.Column == sort.Column {
				return sort
			}
		}
	}
	return Sort{Column: SortDefault}
}

// Missing values are ordered independently of the requested direction.
func sortComparison(value int, leftMissing, rightMissing, descending bool) int {
	if leftMissing != rightMissing {
		if leftMissing {
			return 1
		}
		return -1
	}
	if leftMissing {
		return 0
	}
	if descending {
		return -value
	}
	return value
}

// SortPlanRows sorts a copy, leaving warning slots and equal-key order intact.
func SortPlanRows(rows []monitor.Row, sort Sort) []monitor.Row {
	result := slices.Clone(rows)
	sort = NormalizeSort(PagePlans, sort)
	if sort.IsDefault() {
		return result
	}
	plans := make([]monitor.Row, 0, len(rows))
	for _, row := range rows {
		if row.Kind != monitor.RowKindRepositoryWarning {
			plans = append(plans, row)
		}
	}
	slices.SortStableFunc(plans, func(a, b monitor.Row) int {
		value := 0
		am, bm := false, false
		switch sort.Column {
		case SortRepo:
			value = cmp.Or(cmp.Compare(a.RepositoryName, b.RepositoryName), cmp.Compare(a.RepositoryID, b.RepositoryID))
		case SortNext:
			value = cmp.Compare(planNextActionDisplay(a), planNextActionDisplay(b))
		case SortPlan:
			value = cmp.Compare(rowlabel.PlanLabel(a), rowlabel.PlanLabel(b))
		case SortPriority:
			ap, bp := a.Overview.Priority, b.Overview.Priority
			am, bm = ap == nil, bp == nil
			if !am && !bm {
				value = cmp.Or(cmp.Compare(prioritySortRank(string(ap.Impact)), prioritySortRank(string(bp.Impact))), cmp.Compare(prioritySortRank(string(ap.Risk)), prioritySortRank(string(bp.Risk))), cmp.Compare(prioritySortRank(string(ap.Effort)), prioritySortRank(string(bp.Effort))))
			}
		case SortSlices:
			ac, at := max(a.OriginalCompletedCount+a.ReworkCompletedCount, 0), max(a.OriginalTotalCount+a.ReworkTotalCount, 0)
			bc, bt := max(b.OriginalCompletedCount+b.ReworkCompletedCount, 0), max(b.OriginalTotalCount+b.ReworkTotalCount, 0)
			am, bm = at == 0, bt == 0
			if !am && !bm {
				value = cmp.Or(cmp.Compare(float64(ac)/float64(at), float64(bc)/float64(bt)), cmp.Compare(at-ac, bt-bc))
			}
		case SortRun:
			am, bm = !hasVisibleRun(a), !hasVisibleRun(b)
			value = cmp.Compare(a.InvocationDuration, b.InvocationDuration)
		case SortAge:
			am, bm = a.UpdatedAt == nil, b.UpdatedAt == nil
			if !am && !bm {
				value = a.UpdatedAt.Compare(*b.UpdatedAt)
			}
		}
		return sortComparison(value, am, bm, sort.Descending)
	})
	index := 0
	for i := range result {
		if result[i].Kind != monitor.RowKindRepositoryWarning {
			result[i] = plans[index]
			index++
		}
	}
	return result
}

func prioritySortRank(value string) int {
	switch value {
	case "low", "small":
		return 0
	case "medium":
		return 1
	case "high", "large":
		return 2
	}
	return 3
}

func noteSortTags(item note.CatalogNote) string {
	tags := make([]string, 0, len(item.Tags))
	for _, tag := range item.Tags {
		if !isNoteTierTag(tag) {
			if tag = singleLineNoteValue(tag); tag != "" {
				tags = append(tags, tag)
			}
		}
	}
	return strings.Join(tags, ", ")
}

// SortNotes preserves the caller's default order for equal keys.
func SortNotes(items []note.CatalogNote, sort Sort) []note.CatalogNote {
	result := slices.Clone(items)
	sort = NormalizeSort(PageNotes, sort)
	if sort.IsDefault() {
		return result
	}
	slices.SortStableFunc(result, func(a, b note.CatalogNote) int {
		value := 0
		am, bm := false, false
		switch sort.Column {
		case SortRepo:
			value = cmp.Or(cmp.Compare(a.RepositoryName, b.RepositoryName), cmp.Compare(a.RepositoryID, b.RepositoryID))
		case SortPreview:
			value = cmp.Compare(a.Text, b.Text)
		case SortTags:
			value = cmp.Compare(noteSortTags(a), noteSortTags(b))
		case SortTier:
			value = cmp.Compare(noteTierRank(a), noteTierRank(b))
		case SortCreated:
			am, bm = a.CreatedAt.IsZero(), b.CreatedAt.IsZero()
			value = a.CreatedAt.Compare(b.CreatedAt)
		case SortUpdated:
			am, bm = a.UpdatedAt.IsZero(), b.UpdatedAt.IsZero()
			value = a.UpdatedAt.Compare(b.UpdatedAt)
		}
		return sortComparison(value, am, bm, sort.Descending)
	})
	return result
}

package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
)

const (
	detailLogTailLines = 200
	detailLogKeepLines = 1000
)

// DetailRepository is the read-only plan and log boundary used by the detail
// page. FileRepository satisfies it, while tests can inject a follower without
// touching plan artifacts.
type DetailRepository interface {
	plan.Resolver
	plan.LogTailReader
	plan.LogFollower
}

// DetailModel contains the render-neutral state for one detail frame.
type DetailModel struct {
	Plan        *plan.PlanDetail
	Row         monitor.Row
	Log         string
	Width       int
	Height      int
	UseColor    bool
	LoadError   string
	FollowError string
}

type detailState struct {
	row         monitor.Row
	plan        *plan.PlanDetail
	log         string
	loadError   string
	followError string
	updates     <-chan detailFollowUpdate
	cancel      context.CancelFunc
}

type detailFollowUpdate struct {
	text string
	err  error
}

// RenderDetail builds one complete detail-page frame without writing it.
func RenderDetail(model DetailModel) string {
	id, title, repoName, status := detailHeaderValues(model)
	phase := displayValue(strings.TrimSpace(string(model.Row.Phase)))
	heartbeat := "-"
	if model.Row.Liveness == monitor.LivenessLive || model.Row.Liveness == monitor.LivenessStale {
		heartbeat = durationLabel(model.Row.HeartbeatAge) + " ago"
	}
	header := []string{
		"Tao UI | PLAN DETAIL",
		"Plan: " + id + "  " + title,
		"Repo: " + repoName + "  Status: " + status + "  Phase: " + phase + "  Heartbeat: " + heartbeat,
	}

	available := model.Height - len(header) - 3 // two pane headings and the footer
	if model.Height <= 0 {
		available = 24
	}
	if available < 0 {
		available = 0
	}

	allSliceLines := RenderSlicesPane(model.Plan, model.Width, int(^uint(0)>>1), model.UseColor)
	sliceHeight := min(len(allSliceLines), max(1, available/2))
	if len(allSliceLines) < available {
		sliceHeight = len(allSliceLines)
	}
	if available == 0 {
		sliceHeight = 0
	}
	logHeight := max(available-sliceHeight, 0)

	lines := append([]string(nil), header...)
	lines = append(lines, "SLICES")
	if model.LoadError != "" {
		lines = append(lines, truncateANSI("  unable to load plan: "+model.LoadError, model.Width))
	} else {
		lines = append(lines, RenderSlicesPane(model.Plan, model.Width, sliceHeight, model.UseColor)...)
	}
	lines = append(lines, "LOG (tail)")
	logLines := RenderLogPane(model.Log, model.Width, logHeight)
	if len(logLines) == 0 && logHeight > 0 {
		logLines = []string{"  No agent log output."}
	}
	if model.FollowError != "" && logHeight > 0 {
		logLines = append(logLines, "  log follow stopped: "+model.FollowError)
		if len(logLines) > logHeight {
			logLines = logLines[len(logLines)-logHeight:]
		}
	}
	lines = append(lines, logLines...)
	lines = append(lines, "Esc back")

	if model.Width > 0 {
		for index := range lines {
			lines[index] = truncateANSI(lines[index], model.Width)
		}
	}
	if model.Height > 0 && len(lines) > model.Height {
		footer := lines[len(lines)-1]
		if model.Height == 1 {
			lines = []string{footer}
		} else {
			lines = append(lines[:model.Height-1], footer)
		}
	}
	frame := clearScreenSequence + strings.Join(lines, "\n")
	if model.Height <= 0 || len(lines) < model.Height {
		frame += "\n"
	}
	return frame
}

func detailHeaderValues(model DetailModel) (id, title, repoName, status string) {
	id = model.Row.PlanID
	title = model.Row.PlanTitle
	repoName = model.Row.RepositoryName
	status = model.Row.Status
	if model.Plan != nil {
		if model.Plan.State.Plan.ID != "" {
			id = model.Plan.State.Plan.ID
		}
		if model.Plan.State.Plan.Title != "" {
			title = model.Plan.State.Plan.Title
		}
		if model.Plan.State.Repo.Name != "" {
			repoName = model.Plan.State.Repo.Name
		}
		if model.Plan.State.Status != "" {
			status = model.Plan.State.Status
		}
	}
	return displayValue(id), displayValue(title), displayValue(repoName), displayValue(status)
}

// RenderSlicesPane renders queue-authoritative slice order. The slices.json
// array is only an ID lookup; completed_slices followed by pending_slices owns
// presentation order.
func RenderSlicesPane(detail *plan.PlanDetail, width, height int, useColor bool) []string {
	if detail == nil {
		return nil
	}
	ordered := orderedDetailSlices(detail)
	if len(ordered) == 0 {
		return fitDetailPane([]string{"  No slices."}, width, height, 0)
	}

	statusWidth := 0
	for _, slice := range ordered {
		statusWidth = max(statusWidth, utf8.RuneCountInString(displayValue(slice.Status)))
	}
	currentID := ""
	if detail.State.Plan.CurrentSlice != nil {
		currentID = *detail.State.Plan.CurrentSlice
	}
	lines := make([]string, 0, len(ordered))
	currentLine := 0
	for _, slice := range ordered {
		cursor := "  "
		if slice.ID == currentID {
			cursor = "> "
			currentLine = len(lines)
		}
		status := padRunes(displayValue(slice.Status), statusWidth)
		if useColor {
			status = colorStatus(status, slice.Status)
		}
		line := cursor + status + "  "
		if marker := approvalMarker(slice.Approval); marker != "" {
			line += marker + "  "
		}
		line += displayValue(slice.ID) + "  " + displayValue(slice.Title)
		lines = append(lines, line)
		if note := strings.TrimSpace(slice.BlockerNote); note != "" {
			lines = append(lines, "    blocker: "+note)
		}
	}
	return fitDetailPane(lines, width, height, currentLine)
}

func orderedDetailSlices(detail *plan.PlanDetail) []plan.Slice {
	byID := make(map[string]plan.Slice, len(detail.Slices.Slices))
	for _, slice := range detail.Slices.Slices {
		byID[slice.ID] = slice
	}
	ids := make([]string, 0, len(detail.State.Plan.CompletedSlices)+len(detail.State.Plan.PendingSlices)+1)
	ids = append(ids, detail.State.Plan.CompletedSlices...)
	ids = append(ids, detail.State.Plan.PendingSlices...)
	if detail.State.Plan.CurrentSlice != nil {
		ids = append(ids, *detail.State.Plan.CurrentSlice)
	}
	seen := make(map[string]struct{}, len(ids))
	ordered := make([]plan.Slice, 0, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		if slice, ok := byID[id]; ok {
			ordered = append(ordered, slice)
		}
	}
	return ordered
}

func approvalMarker(approval *plan.Approval) string {
	if approval == nil || !approval.Required {
		return ""
	}
	if approval.Approved {
		return "[approval: approved]"
	}
	return "[approval required]"
}

func fitDetailPane(lines []string, width, height, focus int) []string {
	if height <= 0 || len(lines) == 0 {
		return nil
	}
	start := 0
	if len(lines) > height {
		start = focus - height/2
		start = max(start, 0)
		start = min(start, len(lines)-height)
		lines = lines[start : start+height]
	}
	result := append([]string(nil), lines...)
	if width > 0 {
		for index := range result {
			result[index] = truncateANSI(result[index], width)
		}
	}
	return result
}

// RenderLogPane presents framed records using the tao log convention, passes
// ordinary lines through, and pins the visible window to the newest output.
func RenderLogPane(text string, width, height int) []string {
	if height <= 0 {
		return nil
	}
	presented := presentPlanLog(text)
	lines := strings.Split(presented, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	if width > 0 {
		for index := range lines {
			lines[index] = truncateANSI(lines[index], width)
		}
	}
	return lines
}

func presentPlanLog(text string) string {
	var out strings.Builder
	for len(text) > 0 {
		newline := strings.IndexByte(text, '\n')
		if newline < 0 {
			if record, ok := logrecord.Parse(text); ok {
				_ = logrecord.Render(&out, record)
			} else {
				out.WriteString(text)
			}
			break
		}
		line := text[:newline]
		text = text[newline+1:]
		if record, ok := logrecord.Parse(line); ok {
			_ = logrecord.Render(&out, record)
		} else {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func tailDetailLog(text string, lines int) string {
	if lines <= 0 || text == "" {
		return text
	}
	trimmed := strings.TrimSuffix(text, "\n")
	parts := strings.Split(trimmed, "\n")
	if len(parts) <= lines {
		return text
	}
	return strings.Join(parts[len(parts)-lines:], "\n") + "\n"
}

type detailUpdateWriter struct {
	ctx     context.Context
	updates chan<- detailFollowUpdate
	pending []byte
}

func (w *detailUpdateWriter) Write(value []byte) (int, error) {
	w.pending = append(w.pending, value...)
	newline := bytes.LastIndexByte(w.pending, '\n')
	if newline < 0 {
		return len(value), nil
	}
	complete := string(append([]byte(nil), w.pending[:newline+1]...))
	w.pending = append([]byte(nil), w.pending[newline+1:]...)
	if err := w.send(complete); err != nil {
		return len(value), err
	}
	return len(value), nil
}

func (w *detailUpdateWriter) Flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	pending := string(w.pending)
	w.pending = nil
	return w.send(pending)
}

func (w *detailUpdateWriter) send(text string) error {
	select {
	case w.updates <- detailFollowUpdate{text: text}:
		return nil
	case <-w.ctx.Done():
		return w.ctx.Err()
	}
}

// replaySkippingWriter removes the initial file replay performed by FollowLog;
// the same bytes have already seeded the page through ReadLogTail.
type replaySkippingWriter struct {
	seed    []byte
	pending []byte
	matched bool
	out     io.Writer
}

func newReplaySkippingWriter(seed string, out io.Writer) *replaySkippingWriter {
	return &replaySkippingWriter{seed: []byte(seed), matched: seed == "", out: out}
}

func (w *replaySkippingWriter) Write(value []byte) (int, error) {
	if w.matched {
		_, err := w.out.Write(value)
		return len(value), err
	}
	w.pending = append(w.pending, value...)
	if index := bytes.Index(w.pending, w.seed); index >= 0 {
		remainder := append([]byte(nil), w.pending[index+len(w.seed):]...)
		w.pending = nil
		w.matched = true
		if len(remainder) > 0 {
			if _, err := w.out.Write(remainder); err != nil {
				return len(value), err
			}
		}
		return len(value), nil
	}
	if keep := len(w.seed) - 1; len(w.pending) > keep {
		w.pending = append([]byte(nil), w.pending[len(w.pending)-keep:]...)
	}
	return len(value), nil
}

func followDetailLog(ctx context.Context, repository DetailRepository, planDir, seed string, updates chan<- detailFollowUpdate) {
	defer close(updates)
	appends := &detailUpdateWriter{ctx: ctx, updates: updates}
	writer := newReplaySkippingWriter(seed, appends)
	for {
		err := repository.FollowLog(ctx, planDir, writer)
		if err == nil {
			_ = appends.Flush()
			return
		}
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return
		}
		if !errors.Is(err, os.ErrNotExist) {
			select {
			case updates <- detailFollowUpdate{err: err}:
			case <-ctx.Done():
			}
			return
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

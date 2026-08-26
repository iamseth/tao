package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
)

const (
	detailLogTailLines    = 200
	detailLogKeepLines    = 1000
	detailLogTabWidth     = 4
	planDetailHeaderGap   = 1
	planDetailPaneGap     = 1
	noteDetailHeaderLines = 8
	noteDetailFooter      = "↑/↓/j/k scroll  Bksp/Esc back  q quit"
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
	Plan            *plan.PlanDetail
	Row             monitor.Row
	Log             string
	SliceLog        string
	SelectedSliceID string
	SliceOpen       bool
	Width           int
	Height          int
	UseColor        bool
	ShowShortcuts   bool
	LoadError       string
	FollowError     string
}

type detailState struct {
	row             monitor.Row
	plan            *plan.PlanDetail
	selectedSliceID string
	sliceOpen       bool
	log             string
	sliceLogs       map[string]string
	activeLogSlice  string
	loadError       string
	followError     string
	updates         <-chan detailFollowUpdate
	cancel          context.CancelFunc
}

type detailFollowUpdate struct {
	text string
	err  error
}

// RenderNoteDetail builds a bounded, read-only frame for one open note.
func RenderNoteDetail(item note.CatalogNote, width, height int) string {
	return renderNoteDetail(item, width, height, 0)
}

func renderNoteDetail(item note.CatalogNote, width, height, offset int) string {
	header, body := noteDetailSections(item, width)
	bodyHeight := noteDetailBodyHeight(len(body), height)
	offset = max(0, min(offset, len(body)-bodyHeight))
	lines := append([]string(nil), header...)
	lines = append(lines, body[offset:offset+bodyHeight]...)
	lines = append(lines, noteDetailFooter)
	return fitDetailFrame(lines, width, height)
}

func noteDetailSections(item note.CatalogNote, width int) (header, body []string) {
	created := "-"
	if !item.CreatedAt.IsZero() {
		created = item.CreatedAt.Format(time.RFC3339)
	}
	updated := "-"
	if !item.UpdatedAt.IsZero() {
		updated = item.UpdatedAt.Format(time.RFC3339)
	}
	header = []string{
		"Tao UI | NOTE DETAIL",
		"Repository: " + displayValue(singleLineNoteValue(item.RepositoryName)),
		"Note: " + displayValue(singleLineNoteValue(item.ID)),
		"Status: open",
		"Tags: " + displayValue(singleLineNoteValue(strings.Join(item.Tags, ", "))),
		"Created: " + created,
		"Updated: " + updated,
		"Text:",
	}
	return header, renderNoteText(item.Text, width)
}

func noteDetailBodyHeight(bodyLines, height int) int {
	if height <= 0 {
		return bodyLines
	}
	return max(0, min(bodyLines, height-noteDetailHeaderLines-1))
}

func renderNoteText(text string, width int) []string {
	text = sanitizeNoteText(text)
	if text == "" {
		return []string{"  -"}
	}
	available := width - 2
	if width <= 0 {
		available = 0
	} else {
		available = max(available, 1)
	}
	var lines []string
	for _, source := range strings.Split(text, "\n") {
		runes := []rune(source)
		if available <= 0 || len(runes) <= available {
			lines = append(lines, "  "+source)
			continue
		}
		for len(runes) > available {
			lines = append(lines, "  "+string(runes[:available]))
			runes = runes[available:]
		}
		lines = append(lines, "  "+string(runes))
	}
	return lines
}

func sanitizeNoteText(value string) string {
	var printable strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\x1b' && index+1 < len(value) {
			switch value[index+1] {
			case '[':
				index = skipDetailCSI(value, index+2)
				continue
			case ']':
				index = skipDetailOSC(value, index+2)
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		switch r {
		case '\u009b':
			index = skipDetailCSI(value, index+size)
			continue
		case '\u009d':
			index = skipDetailOSC(value, index+size)
			continue
		case '\n':
			printable.WriteRune(r)
		default:
			if unicode.IsPrint(r) {
				printable.WriteRune(r)
			} else {
				printable.WriteByte(' ')
			}
		}
		index += size
	}
	return strings.TrimSpace(printable.String())
}

// RenderDetail builds one complete detail-page frame without writing it.
func RenderDetail(model DetailModel) string {
	if model.SliceOpen {
		return RenderSliceDetail(model)
	}
	id, title, repoName, status := detailHeaderValues(model)
	phase := displayValue(strings.TrimSpace(string(model.Row.Phase)))
	heartbeat := "-"
	if model.Row.Liveness == monitor.LivenessLive || model.Row.Liveness == monitor.LivenessStale {
		heartbeat = durationLabel(model.Row.HeartbeatAge) + " ago"
	}
	header := "Tao UI | " + singleLineDetail(id) + " | " + singleLineDetail(repoName) +
		" | " + singleLineDetail(status) + " | " + singleLineDetail(phase) + " | " + singleLineDetail(heartbeat)

	paneAvailable := model.Height - 1 - planDetailHeaderGap
	if model.Height <= 0 {
		paneAvailable = 24
	}
	paneAvailable = max(paneAvailable, 0)

	descriptionLines := wrapPaneText(title, model.Width)
	desiredDescriptionHeight := max(len(descriptionLines)+2, 3)
	allSliceLines := renderSlicesPane(model.Plan, model.SelectedSliceID, model.Width, int(^uint(0)>>1), model.UseColor)
	if model.Plan == nil && model.LoadError == "" {
		allSliceLines = []string{"Plan details unavailable."}
	}
	if model.LoadError != "" {
		allSliceLines = []string{"unable to load plan: " + singleLineDetail(model.LoadError)}
	}
	desiredSliceHeight := max(len(allSliceLines)+2, 3)

	descriptionHeight := min(desiredDescriptionHeight, paneAvailable)
	sliceHeight := 0
	logHeight := 0
	remaining := paneAvailable - descriptionHeight
	if descriptionHeight == desiredDescriptionHeight && remaining >= planDetailPaneGap+2 {
		remaining -= planDetailPaneGap
		if remaining >= 2+planDetailPaneGap+3 {
			sliceHeight = min(desiredSliceHeight, remaining-planDetailPaneGap-3)
			logHeight = remaining - sliceHeight - planDetailPaneGap
		} else {
			sliceHeight = remaining
		}
	} else if remaining > 0 {
		descriptionHeight = paneAvailable
	}

	lines := []string{header}
	lines = append(lines, make([]string, planDetailHeaderGap)...)
	if descriptionHeight > 0 {
		bodyHeight := max(descriptionHeight-2, 0)
		lines = append(lines, renderPaneBox("DESCRIPTION", fitDetailPane(descriptionLines, model.Width, bodyHeight, 0), model.Width, descriptionHeight)...)
	}
	if sliceHeight > 0 {
		lines = append(lines, make([]string, planDetailPaneGap)...)
		bodyHeight := max(sliceHeight-2, 0)
		var sliceLines []string
		if model.LoadError != "" || model.Plan == nil {
			sliceLines = fitDetailPane(allSliceLines, model.Width, bodyHeight, 0)
		} else {
			sliceLines = renderSlicesPane(model.Plan, model.SelectedSliceID, model.Width, bodyHeight, model.UseColor)
		}
		lines = append(lines, renderPaneBox("SLICES", sliceLines, model.Width, sliceHeight)...)
	}
	if logHeight > 0 {
		lines = append(lines, make([]string, planDetailPaneGap)...)
		lines = append(lines, renderLogBox(model.Log, model.FollowError, model.Width, logHeight)...)
	}

	if model.Width > 0 {
		for index := range lines {
			lines[index] = truncateANSI(lines[index], model.Width)
		}
	}
	if model.Height > 0 && len(lines) > model.Height {
		lines = lines[:model.Height]
	}
	if model.ShowShortcuts {
		lines = overlayPlanDetailShortcuts(lines, model.Width, model.Height, model.UseColor)
	}
	frame := clearScreenSequence + strings.Join(lines, "\n")
	if model.Height <= 0 || len(lines) < model.Height {
		frame += "\n"
	}
	return frame
}

func wrapPaneText(value string, width int) []string {
	value = singleLineDetail(value)
	if value == "" || value == "-" {
		return []string{"No description available."}
	}
	available := width - 4
	if width <= 0 {
		available = 0
	}
	return wrapDetailWords(value, available)
}

func wrapDetailField(label, value string, width, horizontalPadding int) []string {
	value = singleLineDetail(value)
	if value == "" {
		return nil
	}
	prefix := label + ": "
	available := width - horizontalPadding - utf8.RuneCountInString(prefix)
	if width <= 0 {
		available = 0
	}
	wrapped := wrapDetailWords(value, available)
	indent := strings.Repeat(" ", utf8.RuneCountInString(prefix))
	for index := range wrapped {
		if index == 0 {
			wrapped[index] = prefix + wrapped[index]
		} else {
			wrapped[index] = indent + wrapped[index]
		}
	}
	return wrapped
}

func wrapDetailWords(value string, available int) []string {
	if available <= 0 {
		return []string{value}
	}
	var lines []string
	current := ""
	for _, word := range strings.Fields(value) {
		wordRunes := []rune(word)
		if len(wordRunes) > available {
			if current != "" {
				lines = append(lines, current)
			}
			for len(wordRunes) > available {
				lines = append(lines, string(wordRunes[:available]))
				wordRunes = wordRunes[available:]
			}
			current = string(wordRunes)
			continue
		}
		candidate := word
		if current != "" {
			candidate = current + " " + word
		}
		if utf8.RuneCountInString(candidate) <= available {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = word
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func renderLogBox(log, followError string, width, height int) []string {
	if height <= 0 {
		return nil
	}
	bodyHeight := max(height-2, 0)
	content := RenderLogPane(log, 0, bodyHeight)
	if len(content) == 0 && bodyHeight > 0 {
		content = []string{"No agent log output."}
	}
	if followError != "" && bodyHeight > 0 {
		content = append(content, "log follow stopped: "+singleLineDetail(followError))
		if len(content) > bodyHeight {
			content = content[len(content)-bodyHeight:]
		}
	}
	return renderPaneBox("LOG", content, width, height)
}

func renderPaneBox(title string, content []string, width, height int) []string {
	if height <= 0 {
		return nil
	}
	if width > 0 && width < 4 {
		return []string{truncateANSI(title, width)}
	}
	boxWidth := width
	if boxWidth <= 0 {
		boxWidth = utf8.RuneCountInString(title) + 4
		for _, line := range content {
			boxWidth = max(boxWidth, utf8.RuneCountInString(stripANSISequences(line))+4)
		}
		boxWidth = max(boxWidth, 40)
	}
	if height == 1 {
		return []string{paneBoxTop(title, boxWidth)}
	}
	bodyHeight := max(height-2, 0)
	innerWidth := max(boxWidth-4, 0)
	lines := []string{paneBoxTop(title, boxWidth)}
	for len(content) < bodyHeight {
		content = append(content, "")
	}
	if len(content) > bodyHeight {
		content = content[:bodyHeight]
	}
	for _, line := range content {
		line = truncateANSI(line, innerWidth)
		visible := utf8.RuneCountInString(stripANSISequences(line))
		lines = append(lines, "│ "+line+strings.Repeat(" ", max(innerWidth-visible, 0))+" │")
	}
	lines = append(lines, "└"+strings.Repeat("─", boxWidth-2)+"┘")
	return lines
}

func paneBoxTop(title string, width int) string {
	label := " " + title + " "
	inside := width - 2
	if inside <= 0 {
		return truncatePlain(title, width)
	}
	if utf8.RuneCountInString(label) > inside {
		return "┌" + strings.Repeat("─", inside) + "┐"
	}
	return "┌" + label + strings.Repeat("─", inside-utf8.RuneCountInString(label)) + "┐"
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
	return renderSlicesPane(detail, "", width, height, useColor)
}

func renderSlicesPane(detail *plan.PlanDetail, selectedID string, width, height int, useColor bool) []string {
	if detail == nil {
		return nil
	}
	ordered := orderedDetailSlices(detail)
	if len(ordered) == 0 {
		return fitDetailPane([]string{"  No slices."}, width, height, 0)
	}

	statusWidth := 0
	idWidth := 0
	for _, slice := range ordered {
		statusWidth = max(statusWidth, utf8.RuneCountInString(displayValue(slice.Status)))
		idWidth = max(idWidth, utf8.RuneCountInString(displayValue(slice.ID)))
	}
	if selectedID == "" && detail.State.Plan.CurrentSlice != nil {
		selectedID = *detail.State.Plan.CurrentSlice
	}
	if selectedID == "" && len(ordered) > 0 {
		selectedID = ordered[0].ID
	}
	lines := make([]string, 0, len(ordered))
	selectedLine := 0
	for _, slice := range ordered {
		cursor := "  "
		if slice.ID == selectedID {
			cursor = "> "
			selectedLine = len(lines)
		}
		status := padRunes(displayValue(slice.Status), statusWidth)
		if useColor {
			status = colorStatus(status, slice.Status)
		}
		id := padRunes(displayValue(slice.ID), idWidth)
		line := cursor + status + "  " + id + "  " + displayValue(slice.Title)
		if marker := approvalMarker(slice.Approval); marker != "" {
			line += "  " + marker
		}
		lines = append(lines, line)
		if note := strings.TrimSpace(slice.BlockerNote); note != "" {
			titleColumn := 2 + statusWidth + 2 + idWidth + 2
			lines = append(lines, strings.Repeat(" ", titleColumn)+"blocker: "+note)
		}
	}
	return fitDetailPane(lines, width, height, selectedLine)
}

// RenderSliceDetail renders the selected slice as a bounded read-only frame.
func RenderSliceDetail(model DetailModel) string {
	selected, ok := findDetailSlice(model.Plan, model.SelectedSliceID)
	id, status, approval, goal := "-", "-", "-", "-"
	var details []string
	if ok {
		id = displayValue(singleLineDetail(selected.ID))
		status = displayValue(singleLineDetail(selected.Status))
		approval = "not required"
		if selected.Approval != nil && selected.Approval.Required {
			approval = "required"
		}
		if selected.Approval != nil && selected.Approval.Approved {
			approval = "approved"
		}
		if value := singleLineDetail(selected.Goal); value != "" {
			goal = value
		}
		appendDetailList(&details, "Tasks", selected.Tasks)
		appendDetailList(&details, "Expected files", selected.ExpectedFiles)
		appendDetailList(&details, "Verification commands", selected.Verification.Commands)
		appendDetailValue(&details, "Blocker", selected.BlockerNote)
		appendDetailValue(&details, "Notes", selected.Notes)
		if len(selected.VerificationResults) > 0 {
			values := make([]string, 0, len(selected.VerificationResults))
			for _, result := range selected.VerificationResults {
				value := singleLineDetail(result.Command)
				if result.Result != "" {
					value += ": " + result.Result
				}
				if result.Details != "" {
					value += " — " + result.Details
				}
				values = append(values, value)
			}
			appendDetailList(&details, "Verification results", values)
		}
		if selected.Completion != nil {
			value := selected.Completion.Outcome
			if selected.Completion.CommitSHA != "" {
				value += " (" + selected.Completion.CommitSHA + ")"
			}
			appendDetailValue(&details, "Commit outcome", value)
		}
	}
	if len(details) == 0 {
		details = []string{"No additional details."}
	}
	goalLines := wrapDetailField("Goal", goal, model.Width, 0)

	filteredLog := model.SliceLog
	if filteredLog == "" {
		filteredLog = filterSliceLog(model.Log, id)
	}
	allLogLines := RenderLogPane(filteredLog, 0, int(^uint(0)>>1))
	desiredLogHeight := max(len(allLogLines)+2, 3)

	desiredDetailHeight := max(len(details)+2, 3)
	detailHeight := desiredDetailHeight
	logHeight := desiredLogHeight
	if model.Height > 0 {
		fixedHeight := 1 + planDetailHeaderGap + len(goalLines) + planDetailPaneGap
		available := max(model.Height-fixedHeight, 0)
		detailHeight = min(desiredDetailHeight, available)
		logHeight = 0
		if available >= 2+planDetailPaneGap+3 {
			detailHeight = min(desiredDetailHeight, available-planDetailPaneGap-3)
			logHeight = available - detailHeight - planDetailPaneGap
		}
	}

	lines := []string{"Tao UI | " + id + " | " + status + " | approval: " + approval}
	lines = append(lines, make([]string, planDetailHeaderGap)...)
	lines = append(lines, goalLines...)
	if detailHeight > 0 {
		lines = append(lines, make([]string, planDetailPaneGap)...)
		lines = append(lines, renderPaneBox("DETAIL", fitDetailPane(details, model.Width, max(detailHeight-2, 0), 0), model.Width, detailHeight)...)
	}
	if logHeight > 0 {
		lines = append(lines, make([]string, planDetailPaneGap)...)
		lines = append(lines, renderLogBox(filteredLog, "", model.Width, logHeight)...)
	}
	if model.Width > 0 {
		for index := range lines {
			lines[index] = truncateANSI(lines[index], model.Width)
		}
	}
	if model.Height > 0 && len(lines) > model.Height {
		lines = lines[:model.Height]
	}
	if model.ShowShortcuts {
		lines = overlaySliceDetailShortcuts(lines, model.Width, model.Height, model.UseColor)
	}
	frame := clearScreenSequence + strings.Join(lines, "\n")
	if model.Height <= 0 || len(lines) < model.Height {
		frame += "\n"
	}
	return frame
}

func findDetailSlice(detail *plan.PlanDetail, id string) (plan.Slice, bool) {
	if detail == nil {
		return plan.Slice{}, false
	}
	for _, slice := range orderedDetailSlices(detail) {
		if slice.ID == id {
			return slice, true
		}
	}
	return plan.Slice{}, false
}

func appendDetailValue(lines *[]string, label, value string) {
	if value = singleLineDetail(value); value != "" {
		*lines = append(*lines, label+": "+value)
	}
}

func appendDetailList(lines *[]string, label string, values []string) {
	added := false
	for _, value := range values {
		if value = singleLineDetail(value); value != "" {
			if !added {
				*lines = append(*lines, label+":")
				added = true
			}
			*lines = append(*lines, "  - "+value)
		}
	}
}

func singleLineDetail(value string) string {
	var printable strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\x1b' && index+1 < len(value) {
			switch value[index+1] {
			case '[':
				index = skipDetailCSI(value, index+2)
				printable.WriteByte(' ')
				continue
			case ']':
				index = skipDetailOSC(value, index+2)
				printable.WriteByte(' ')
				continue
			}
		}

		r, size := utf8.DecodeRuneInString(value[index:])
		switch r {
		case '\u009b':
			index = skipDetailCSI(value, index+size)
			printable.WriteByte(' ')
			continue
		case '\u009d':
			index = skipDetailOSC(value, index+size)
			printable.WriteByte(' ')
			continue
		}
		if unicode.IsPrint(r) {
			printable.WriteRune(r)
		} else {
			printable.WriteByte(' ')
		}
		index += size
	}
	return strings.Join(strings.Fields(printable.String()), " ")
}

func skipDetailCSI(value string, index int) int {
	for index < len(value) {
		if value[index] >= '@' && value[index] <= '~' {
			return index + 1
		}
		index++
	}
	return len(value)
}

func skipDetailOSC(value string, index int) int {
	for index < len(value) {
		if value[index] == '\a' {
			return index + 1
		}
		if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '\\' {
			return index + 2
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		if r == '\u009c' {
			return index + size
		}
		index += size
	}
	return len(value)
}

func fitDetailFrame(lines []string, width, height int) string {
	if width > 0 {
		for index := range lines {
			lines[index] = truncateANSI(lines[index], width)
		}
	}
	if height > 0 && len(lines) > height {
		footer := lines[len(lines)-1]
		if height == 1 {
			lines = []string{footer}
		} else {
			lines = append(lines[:height-1], footer)
		}
	}
	frame := clearScreenSequence + strings.Join(lines, "\n")
	if height <= 0 || len(lines) < height {
		frame += "\n"
	}
	return frame
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
	for index := range lines {
		lines[index] = strings.ReplaceAll(lines[index], "\t", strings.Repeat(" ", detailLogTabWidth))
		if width > 0 {
			lines[index] = truncateANSI(lines[index], width)
		}
	}
	return lines
}

func projectSliceLogs(text string, keepLines int) (map[string]string, string) {
	logs := make(map[string]string)
	active := ""
	appendSliceLogRecords(logs, &active, text, keepLines)
	return logs, active
}

func appendSliceLogRecords(logs map[string]string, active *string, text string, keepLines int) {
	for len(text) > 0 {
		newline := strings.IndexByte(text, '\n')
		line := text
		suffix := ""
		if newline >= 0 {
			line = text[:newline]
			suffix = "\n"
			text = text[newline+1:]
		} else {
			text = ""
		}
		if record, ok := logrecord.Parse(line); ok && record.Type == logrecord.TypeSession {
			*active = runningSliceID(record.Content)
		}
		if *active == "" {
			continue
		}
		logs[*active] = tailDetailLog(logs[*active]+line+suffix, keepLines)
	}
}

func runningSliceID(action string) string {
	const prefix = "running "
	action = strings.TrimSpace(action)
	if !strings.HasPrefix(action, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(action, prefix))
}

func filterSliceLog(text, sliceID string) string {
	if strings.TrimSpace(sliceID) == "" || sliceID == "-" {
		return ""
	}
	var filtered strings.Builder
	active := false
	for len(text) > 0 {
		newline := strings.IndexByte(text, '\n')
		line := text
		suffix := ""
		if newline >= 0 {
			line = text[:newline]
			suffix = "\n"
			text = text[newline+1:]
		} else {
			text = ""
		}
		if record, ok := logrecord.Parse(line); ok && record.Type == logrecord.TypeSession {
			active = strings.TrimSpace(record.Content) == "running "+sliceID
		}
		if active {
			filtered.WriteString(line)
			filtered.WriteString(suffix)
		}
	}
	return filtered.String()
}

func presentPlanLog(text string) string {
	var out strings.Builder
	for len(text) > 0 {
		newline := strings.IndexByte(text, '\n')
		if newline < 0 {
			if record, ok := logrecord.Parse(text); ok {
				out.WriteString(presentLogRecord(record))
			} else {
				out.WriteString(text)
			}
			break
		}
		line := text[:newline]
		text = text[newline+1:]
		if record, ok := logrecord.Parse(line); ok {
			out.WriteString(presentLogRecord(record))
		} else {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func presentLogRecord(record logrecord.Record) string {
	var rendered strings.Builder
	if record.Type == logrecord.TypeSession {
		rendered.WriteString("--- " + singleLineDetail(record.Content) + " ---\n")
	} else {
		_ = logrecord.Render(&rendered, record)
	}
	timestamp := logTimestamp(record.Timestamp)
	if timestamp == "" {
		return rendered.String()
	}
	lines := strings.Split(strings.TrimSuffix(rendered.String(), "\n"), "\n")
	var presented strings.Builder
	for _, line := range lines {
		presented.WriteString("[" + timestamp + "] " + line + "\n")
	}
	return presented.String()
}

func logTimestamp(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return ""
	}
	return parsed.Format("15:04:05")
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
		w.seed = nil
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

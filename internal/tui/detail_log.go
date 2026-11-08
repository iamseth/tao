package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/term/cells"
)

const (
	detailLogTailLines = 200
	detailLogKeepLines = 1000
	detailLogTabWidth  = 4
)

type detailFollowUpdate struct {
	text string
	err  error
}

func renderActivityPane(log, followError string, width, height, offset int) []string {
	lines := RenderLogPane(log, 0, int(^uint(0)>>1))
	if len(lines) == 0 {
		lines = []string{"No agent log output."}
	}
	if followError != "" {
		lines = append(lines, "log follow stopped: "+singleLineDetail(followError))
	}
	return fitDetailPaneAt(lines, width, height, offset)
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
			lines[index] = cells.Truncate(lines[index], width)
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

package merge

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/iamseth/tao/internal/agent/logrecord"
)

// BatchTranscriptWriter lazily appends agent records to the active batch's
// audit directory. Transcript failures never interrupt the provider stream;
// Close reports them to callers that want diagnostics, not recovery authority.
// It must be closed after the batch invocation, including failed invocations.
type BatchTranscriptWriter struct {
	mu        sync.Mutex
	store     *BatchStore
	out       io.Writer
	now       func() time.Time
	id        string
	file      *os.File
	writer    io.Writer
	err       error
	logFailed bool
	closed    bool
}

var _ io.WriteCloser = (*BatchTranscriptWriter)(nil)

func NewBatchTranscriptWriter(store *BatchStore, out io.Writer, now func() time.Time) *BatchTranscriptWriter {
	if out == nil {
		out = io.Discard
	}
	return &BatchTranscriptWriter{store: store, out: out, now: now}
}

func (w *BatchTranscriptWriter) Write(p []byte) (int, error) {
	if w == nil {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.store == nil {
		return w.out.Write(p)
	}
	id, err := w.store.ActiveID()
	if err != nil || id == "" {
		return w.out.Write(p)
	}
	if id != w.id {
		if w.file != nil {
			w.err = errors.Join(w.err, w.file.Close())
		}
		w.id, w.file, w.writer, w.logFailed = id, nil, nil, false
		path := w.store.TranscriptPath(id)
		err = os.MkdirAll(filepath.Dir(path), 0o700)
		if err == nil {
			w.file, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- batch audit path is supplied by the repository-scoped store.
		}
		if err != nil {
			w.err = errors.Join(w.err, err)
		} else {
			w.writer = logrecord.TimestampWriter(logrecord.TeeWriter(batchTranscriptFile{w}, w.out), w.now)
			// The header is presentation-only too: failure must not suppress agent output.
			_ = logrecord.Write(w.writer, logrecord.Record{Type: logrecord.TypeSession, Content: "merge batch " + id})
		}
	}
	if w.writer == nil {
		return w.out.Write(p)
	}
	return w.writer.Write(p)
}

// batchTranscriptFile keeps a failed audit write from preventing TeeWriter's
// terminal rendering. Write holds the owner's mutex throughout both outputs.
type batchTranscriptFile struct{ owner *BatchTranscriptWriter }

func (f batchTranscriptFile) Write(p []byte) (int, error) {
	w := f.owner
	if !w.logFailed {
		n, err := w.file.Write(p)
		if err == nil && n != len(p) {
			err = io.ErrShortWrite
		}
		if err != nil {
			w.err = errors.Join(w.err, err)
			w.logFailed = true
		}
	}
	return len(p), nil
}

// Close is idempotent, retaining any audit error without reopening on later writes.
func (w *BatchTranscriptWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		if w.file != nil {
			w.err = errors.Join(w.err, w.file.Close())
		}
	}
	return w.err
}

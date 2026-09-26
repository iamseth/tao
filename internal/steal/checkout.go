package steal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/iamseth/tao/internal/gitops"
)

// Check every tree entry, not unique objects: a small compressed blob can be
// repeated at many paths. Stream only type/size metadata so neither file names
// nor the expanded tree listing need to be buffered before rejecting a clone.
func checkCheckoutSize(ctx context.Context, root string, runner gitops.Runner, maxBytes int64) error {
	counter := checkoutSizeWriter{remaining: maxBytes}
	err := runner(ctx, root, "git", []string{"ls-tree", "-r", "-z", "--format=%(objecttype) %(objectsize)", "HEAD"}, &counter, io.Discard)
	if counter.err != nil {
		return counter.err
	}
	if err != nil {
		return fmt.Errorf("measure checkout tree: %w", err)
	}
	if len(counter.pending) != 0 {
		return errors.New("incomplete checkout tree size record")
	}
	return nil
}

type checkoutSizeWriter struct {
	remaining int64
	pending   []byte
	err       error
}

func (w *checkoutSizeWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 && w.err == nil {
		end := bytes.IndexByte(p, 0)
		n := end
		if n < 0 {
			n = len(p)
		}
		if len(w.pending)+n > 128 {
			w.err = errors.New("oversized checkout tree size record")
			break
		}
		w.pending = append(w.pending, p[:n]...)
		written += n
		p = p[n:]
		if end < 0 {
			break
		}
		written++
		p = p[1:]
		w.err = w.count(string(w.pending))
		w.pending = w.pending[:0]
	}
	return written, w.err
}

func (w *checkoutSizeWriter) count(record string) error {
	// Submodule commits are not materialized; symlinks are blobs and count
	// conservatively even though hardening later strips them.
	if record == "commit -" {
		return nil
	}
	typ, value, ok := strings.Cut(record, " ")
	size, err := strconv.ParseInt(value, 10, 64)
	if !ok || typ != "blob" || err != nil || size < 0 {
		return errors.New("invalid checkout tree size record")
	}
	if size > w.remaining {
		return errors.New("expanded checkout tree exceeds snapshot cap")
	}
	w.remaining -= size
	return nil
}

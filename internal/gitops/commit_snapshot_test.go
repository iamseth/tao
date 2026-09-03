package gitops

import (
	"context"
	"errors"
	"hash"
	"io"
	"strings"
	"testing"
)

func TestCommitPathStatesOversizedBlobIsStreamedAndCancellable(t *testing.T) {
	const (
		chunkSize  = 32 * 1024
		chunkCount = 2048 // 64 MiB of logical blob output without a 64 MiB fixture.
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var streamed int64
	var sawIncrementalHash bool
	runner := func(ctx context.Context, _ string, name string, args []string, stdout io.Writer, _ io.Writer) error {
		if name != "git" || len(args) < 3 || args[0] != "-C" || args[1] != "/repo" {
			return errors.New("unexpected command")
		}
		switch strings.Join(args[2:], "\x00") {
		case "diff-tree\x00-r\x00--no-commit-id\x00--no-renames\x00--name-status\x00-z\x00parent\x00commit":
			_, _ = io.WriteString(stdout, "M\x00large.bin\x00")
			return nil
		case "ls-tree\x00-z\x00commit\x00--\x00large.bin":
			_, _ = io.WriteString(stdout, "100644 blob object-id\tlarge.bin\x00")
			return nil
		case "cat-file\x00blob\x00object-id":
			_, sawIncrementalHash = stdout.(hash.Hash)
			if !sawIncrementalHash {
				return errors.New("blob output was not streamed into an incremental hash")
			}
			chunk := make([]byte, chunkSize)
			for i := 0; ; i++ {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
				n, err := stdout.Write(chunk)
				streamed += int64(n)
				if err != nil {
					return err
				}
				if i+1 == chunkCount {
					cancel()
				}
			}
		default:
			return errors.New("unexpected git arguments")
		}
	}

	_, err := NewClient("/repo", runner).CommitPathStates(ctx, "parent", "commit")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CommitPathStates error = %v, want context cancellation", err)
	}
	if !sawIncrementalHash {
		t.Fatal("oversized blob was not sent to an incremental hash")
	}
	if want := int64(chunkSize * chunkCount); streamed != want {
		t.Fatalf("streamed bytes = %d, want %d before cancellation", streamed, want)
	}
}

package plandelta

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"strconv"

	"github.com/iamseth/tao/internal/gitops"
)

// Each field is length-framed, preserving exact path bytes and boundaries.
// Only filtered authoritative records belong here, never raw Git captures.
// Metadata is intentionally absent: consumers replace it on every collection.
func snapshotSignature(s Snapshot, files []FileChange, extraRecords ...string) string {
	h := sha256.New()
	for _, field := range []string{"plandelta-v1", string(s.Scope), s.Base.SHA, s.Head, s.DefaultHead, strconv.FormatBool(s.FilesTruncated)} {
		signatureField(h, field)
	}
	signatureField(h, strconv.Itoa(len(files)))
	for _, file := range files {
		for _, field := range []string{file.Path, file.OldPath, string(file.Status), strconv.Itoa(file.Added), strconv.Itoa(file.Deleted), strconv.FormatBool(file.Binary), strconv.FormatBool(file.Untracked), strconv.FormatBool(file.Uncommitted), strconv.FormatBool(file.RevertsCommitted)} {
			signatureField(h, field)
		}
	}
	signatureField(h, strconv.Itoa(len(extraRecords)))
	for _, record := range extraRecords {
		signatureField(h, record)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Positive literal pathspecs exclude metadata without weakening the client's
// literal-path guard. Batch argv and stream patches, including binary payloads.
func worktreeDigests(ctx context.Context, git gitops.Client, head string, files []FileChange) ([]string, error) {
	var digests []string
	for start := 0; start < len(files); start += 100 {
		end := min(start+100, len(files))
		args := []string{"--binary", head, "--"}
		for _, file := range files[start:end] {
			args = append(args, file.Path)
		}
		digest, err := git.DiffDigest(ctx, args...)
		if err != nil {
			return nil, err
		}
		digests = append(digests, digest)
	}
	return digests, nil
}

func signatureField(h hash.Hash, field string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(field)))
	_, _ = h.Write(size[:])
	_, _ = h.Write([]byte(field))
}

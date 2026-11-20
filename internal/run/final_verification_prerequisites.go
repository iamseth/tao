package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/iamseth/tao/internal/plan"
)

var (
	ErrFinalPrerequisiteMissing     = errors.New("final verification prerequisite missing")
	ErrFinalPrerequisiteUnsupported = errors.New("final verification prerequisite unsupported or unobservable")
	ErrFinalPrerequisiteUnstable    = errors.New("final verification prerequisite changed during observation")
)

const finalPrerequisiteMaxBytes int64 = 32 << 20
const finalPrerequisiteTotalBytes int64 = 128 << 20

// observeFinalVerificationPrerequisites observes only declared bytes and identities.
// A successful empty result makes no claim about undeclared environment state.
// Any failure discards the entire list; a partial list cannot imply equivalence.
func observeFinalVerificationPrerequisites(ctx context.Context, root, pathEnv string, prerequisites []plan.FinalVerificationPrerequisite) ([]plan.FinalVerificationObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(prerequisites) > 256 {
		return nil, ErrFinalPrerequisiteUnsupported
	}
	if len(prerequisites) == 0 {
		return nil, nil
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		return nil, prerequisiteIOError(err)
	}
	defer func() { _ = base.Close() }()
	var observations []plan.FinalVerificationObservation
	budget := finalPrerequisiteTotalBytes
	for _, p := range prerequisites {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var observation plan.FinalVerificationObservation
		switch p.Kind {
		case "file":
			if !filepath.IsLocal(p.Path) || strings.Contains(p.Path, "\\") {
				return nil, ErrFinalPrerequisiteUnsupported
			}
			for _, part := range strings.Split(p.Path, "/") {
				if part == ".." {
					return nil, ErrFinalPrerequisiteUnsupported
				}
			}
			digest, err := hashFinalPrerequisite(ctx, base, p.Path, false, &budget)
			if err != nil {
				return nil, err
			}
			observation = plan.FinalVerificationObservation{Kind: p.Kind, Identity: filepath.ToSlash(filepath.Clean(p.Path)), Digest: digest}
		case "executable":
			resolved, err := resolveFinalPrerequisiteExecutable(root, pathEnv, p.Name)
			if err != nil {
				return nil, err
			}
			// Executables may live outside the repository. OpenRoot still pins their
			// directory, and the subsequent resolution check catches PATH substitution.
			dir, err := os.OpenRoot(filepath.Dir(resolved))
			if err != nil {
				return nil, prerequisiteIOError(err)
			}
			digest, hashErr := hashFinalPrerequisite(ctx, dir, filepath.Base(resolved), true, &budget)
			_ = dir.Close()
			if hashErr != nil {
				return nil, hashErr
			}
			again, err := resolveFinalPrerequisiteExecutable(root, pathEnv, p.Name)
			if err != nil || again != resolved {
				return nil, ErrFinalPrerequisiteUnstable
			}
			observation = plan.FinalVerificationObservation{Kind: p.Kind, Identity: resolved, Digest: digest}
		default:
			return nil, ErrFinalPrerequisiteUnsupported
		}
		observations = append(observations, observation)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return observations, nil
}

func resolveFinalPrerequisiteExecutable(root, pathEnv, name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return "", ErrFinalPrerequisiteUnsupported
	}
	if pathEnv == "" {
		return "", ErrFinalPrerequisiteMissing
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate) // #nosec G703 -- passive lookup intentionally follows supplied PATH directories; name is a validated basename, never executed.
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", prerequisiteIOError(err)
		}
		if !info.Mode().IsRegular() {
			return "", ErrFinalPrerequisiteUnsupported
		}
		if info.Mode().Perm()&0111 == 0 {
			continue
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", prerequisiteIOError(err)
		}
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return "", ErrFinalPrerequisiteUnsupported
		}
		return resolved, nil
	}
	return "", ErrFinalPrerequisiteMissing
}

func prerequisiteIOError(err error) error {
	// Do not include OS paths or content in diagnostics. Preserve only the class.
	if errors.Is(err, os.ErrNotExist) {
		return ErrFinalPrerequisiteMissing
	}
	return ErrFinalPrerequisiteUnsupported
}

func samePrerequisiteFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.Mode() == b.Mode() && a.ModTime().Equal(b.ModTime())
}

func hashFinalPrerequisite(ctx context.Context, root *os.Root, path string, executable bool, budget *int64) (string, error) {
	before, err := root.Stat(path)
	if err != nil {
		return "", prerequisiteIOError(err)
	}
	if !before.Mode().IsRegular() || before.Size() > finalPrerequisiteMaxBytes || before.Size() > *budget || (executable && before.Mode().Perm()&0111 == 0) {
		return "", ErrFinalPrerequisiteUnsupported
	}
	// Nonblocking open prevents a concurrent replacement with a FIFO from
	// hanging observation. Root confines symlink resolution, including races.
	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", prerequisiteIOError(err)
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !samePrerequisiteFile(before, opened) {
		return "", ErrFinalPrerequisiteUnstable
	}
	*budget -= before.Size()
	var digest string
	// A second bounded pass catches byte changes even when size/mtime are restored.
	// This is a best-effort stable observation, not an atomic filesystem snapshot.
	for pass := 0; pass < 2; pass++ {
		hash := sha256.New()
		remaining := finalPrerequisiteMaxBytes + 1
		buf := make([]byte, 32<<10)
		var count int64
		for remaining > 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			n, readErr := f.Read(buf[:min(int64(len(buf)), remaining)])
			if n > 0 {
				_, _ = hash.Write(buf[:n])
				count += int64(n)
				remaining -= int64(n)
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				return "", prerequisiteIOError(readErr)
			}
		}
		if count != before.Size() {
			return "", ErrFinalPrerequisiteUnstable
		}
		current := hex.EncodeToString(hash.Sum(nil))
		if pass == 1 && current != digest {
			return "", ErrFinalPrerequisiteUnstable
		}
		digest = current
		after, err := f.Stat()
		if err != nil || !samePrerequisiteFile(before, after) {
			return "", ErrFinalPrerequisiteUnstable
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return "", fmt.Errorf("%w: cannot reread", ErrFinalPrerequisiteUnsupported)
		}
	}
	after, err := root.Stat(path)
	if err != nil || !samePrerequisiteFile(before, after) {
		return "", ErrFinalPrerequisiteUnstable
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return digest, nil
}

package commandrunner

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const verificationCacheEnv = "GOLANGCI_LINT_CACHE"

// The rule lives inside golangci-lint, not .tao or cache: sibling metadata and
// unrelated untracked files must remain visible to Git.
const verificationCacheIgnore = "*\n"

type verificationCacheKey struct{}

type verificationCachePolicy struct {
	executionRoot string
}

// WithVerificationCache opts local verification subprocesses into a private
// cache under executionRoot. It only derives a context; DefaultLocal prepares
// storage and overrides the child's environment. Relative roots resolve against
// the process working directory at dispatch, never the command's subdirectory.
func WithVerificationCache(ctx context.Context, executionRoot string) context.Context {
	return context.WithValue(ctx, verificationCacheKey{}, verificationCachePolicy{executionRoot: executionRoot})
}

func verificationCacheEnvironment(ctx context.Context, environ []string) ([]string, error) {
	policy, ok := ctx.Value(verificationCacheKey{}).(verificationCachePolicy)
	if !ok {
		return environ, nil
	}
	cache, err := prepareVerificationCache(policy.executionRoot)
	if err != nil {
		return nil, fmt.Errorf("prepare verification cache: %w", err)
	}
	child := make([]string, 0, len(environ)+1)
	for _, entry := range environ {
		if key, _, _ := strings.Cut(entry, "="); key != verificationCacheEnv {
			child = append(child, entry)
		}
	}
	return append(child, verificationCacheEnv+"="+cache), nil
}

func prepareVerificationCache(executionRoot string) (string, error) {
	if executionRoot == "" {
		return "", errors.New("execution root is empty")
	}
	absolute, err := filepath.Abs(executionRoot)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return "", err
	}
	defer func(opened *os.Root) { _ = opened.Close() }(root)
	// Anchor each operation to an opened directory. Besides rejecting existing
	// symlinks, Root prevents an escaping symlink swap during preparation.
	for _, name := range []string{".tao", "cache", "golangci-lint"} {
		if err := root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%s is not a directory (symlinks are not allowed)", name)
		}
		next, err := root.OpenRoot(name)
		if err != nil {
			return "", err
		}
		defer func() { _ = next.Close() }()
		root = next
	}
	if err := prepareVerificationCacheIgnore(root); err != nil {
		return "", err
	}
	return filepath.Join(absolute, ".tao", "cache", "golangci-lint"), nil
}

func prepareVerificationCacheIgnore(root *os.Root) error {
	// Publish a complete file without replacing existing metadata. Linking is
	// atomic across processes, so concurrent first users cannot see a partial
	// ignore file. Creating the temporary also checks writable cache storage on
	// reuse; a setup failure must never launch a command with a shared cache.
	temporary := ".gitignore-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(temporary) }()
	_, writeErr := io.WriteString(file, verificationCacheIgnore)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if err := root.Link(temporary, ".gitignore"); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := root.Lstat(".gitignore")
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(verificationCacheIgnore)) {
		return errors.New("conflicting verification cache .gitignore")
	}
	ignore, err := root.Open(".gitignore")
	if err != nil {
		return err
	}
	defer func() { _ = ignore.Close() }()
	content, err := io.ReadAll(io.LimitReader(ignore, int64(len(verificationCacheIgnore)+1)))
	if err != nil {
		return err
	}
	if string(content) != verificationCacheIgnore {
		return errors.New("conflicting verification cache .gitignore")
	}
	return nil
}

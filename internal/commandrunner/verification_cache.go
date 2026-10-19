package commandrunner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const verificationCacheEnv = "GOLANGCI_LINT_CACHE"
const verificationBytecodeEnv = "PYTHONDONTWRITEBYTECODE"

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
	return verificationCacheEnvironmentAt(ctx, "", environ)
}

func verificationCacheEnvironmentAt(ctx context.Context, cwd string, environ []string) ([]string, error) {
	policy, ok := ctx.Value(verificationCacheKey{}).(verificationCachePolicy)
	if !ok {
		return environ, nil
	}
	namespace := verificationCacheNamespace(ctx, cwd, environ)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cache, err := prepareVerificationCacheNamespace(policy.executionRoot, namespace)
	if err != nil {
		return nil, fmt.Errorf("prepare verification cache: %w", err)
	}
	child := make([]string, 0, len(environ)+2)
	bytecodeDefined := false
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		if key == verificationBytecodeEnv {
			bytecodeDefined = true
		}
		if key != verificationCacheEnv {
			child = append(child, entry)
		}
	}
	if !bytecodeDefined {
		child = append(child, verificationBytecodeEnv+"=1")
	}
	return append(child, verificationCacheEnv+"="+cache), nil
}

func prepareVerificationCache(executionRoot string) (string, error) {
	return prepareVerificationCacheNamespace(executionRoot, "")
}

func prepareVerificationCacheNamespace(executionRoot, namespace string) (string, error) {
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
	parent := filepath.Join(absolute, ".tao", "cache", "golangci-lint")
	if namespace == "" {
		return parent, nil
	}
	if filepath.Base(namespace) != namespace || namespace == "." || namespace == ".." {
		return "", errors.New("invalid cache namespace")
	}
	if err := root.Mkdir(namespace, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := root.Lstat(namespace)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("cache namespace is not a directory (symlinks are not allowed)")
	}
	next, err := root.OpenRoot(namespace)
	if err != nil {
		return "", err
	}
	defer func() { _ = next.Close() }()
	probe := ".writable-" + rand.Text()
	file, err := next.OpenFile(probe, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, namespace), errors.Join(file.Close(), next.Remove(probe))
}

// Identity probes are deliberately separate from DefaultLocal: they never prepare
// caches or enter a domain runner, and share its cancellation and token policy.
const verificationIdentityLimit = 8192

var verificationGoVersion = regexp.MustCompile(`^go[0-9]+\.[0-9]+[^\s]*$`)

type verificationIdentityOutput struct {
	data     []byte
	overflow bool
}

func (output *verificationIdentityOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := verificationIdentityLimit - len(output.data)
	if n > remaining {
		output.overflow = true
		p = p[:remaining]
	}
	output.data = append(output.data, p...)
	return n, nil
}

// verificationIdentityTimeout bounds each tool identity probe; on expiry the
// cache falls back to a fresh namespace rather than failing verification.
var verificationIdentityTimeout = time.Second

func verificationIdentity(ctx context.Context, cwd string, environ []string, name string, args ...string) (string, bool) {
	return verificationIdentityWithTimeout(ctx, verificationIdentityTimeout, cwd, environ, name, args...)
}

func verificationIdentityWithTimeout(ctx context.Context, timeout time.Duration, cwd string, environ []string, name string, args ...string) (string, bool) {
	// Resolve PATH against the child's cwd, not exec.LookPath's process cwd.
	base, err := filepath.Abs(cwd)
	if err != nil {
		return "", false
	}
	var path string
	for _, entry := range environ {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path = value
		}
	}
	var executable string
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(base, dir)
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 { // #nosec G703 -- PATH lookup intentionally inspects executable candidates outside the worktree.
			executable = candidate
			break
		}
	}
	if executable == "" {
		return "", false
	}
	probe, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(probe, executable, args...) // #nosec G204 G702 -- intentionally execute the dispatch-time tool from the verification PATH, with fixed probe arguments.
	cleanup := configureCommandCancellation(cmd)
	defer cleanup()
	cmd.Dir = cwd
	cmd.Env = withoutSliceCompletionOwner(environ)
	cmd.WaitDelay = 100 * time.Millisecond
	var output verificationIdentityOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil || output.overflow || probe.Err() != nil {
		return "", false
	}
	value := strings.TrimSpace(string(output.data))
	if value == "" || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) {
		return "", false
	}
	return value, true
}

func verificationCacheNamespace(ctx context.Context, cwd string, environ []string) string {
	goVersion, goOK := verificationIdentity(ctx, cwd, environ, "go", "env", "GOVERSION")
	lintVersion, lintOK := verificationIdentity(ctx, cwd, environ, "golangci-lint", "version")
	if !goOK || !lintOK || !verificationGoVersion.MatchString(goVersion) || !strings.HasPrefix(lintVersion, "golangci-lint has version ") || strings.TrimSpace(strings.TrimPrefix(lintVersion, "golangci-lint has version ")) == "" {
		return "fresh-" + rand.Text()
	}
	// Length framing keeps field boundaries unambiguous; the schema prefix
	// permits future identity changes without reusing incompatible entries.
	identity := fmt.Sprintf("tao-lint-cache-v1\x00go:%d:%s\x00lint:%d:%s", len(goVersion), goVersion, len(lintVersion), lintVersion)
	return fmt.Sprintf("v1-%x", sha256.Sum256([]byte(identity)))
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

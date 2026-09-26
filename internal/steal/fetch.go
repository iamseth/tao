package steal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/gitops"
)

type Options struct {
	DataHome string
	Source   Source
	Now      func() time.Time
	Runner   gitops.Runner
	MaxBytes int64
}

type Result struct {
	SnapshotPath, DefaultBranch, Commit, DeclaredVersion, CampaignTag string
	SizeBytes                                                         int64
	Omitted                                                           []Omission
}

func defaultRunner(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
	cloning := name == "git" && slices.Contains(args, "clone")
	env := isolatedGitEnv(cloning)
	if cloning {
		config, err := transportConfig(ctx, cwd)
		if err != nil {
			return err
		}
		// Keep authentication values out of process argv. These entries are
		// rebuilt from the allowlist, never inherited from the caller.
		env = append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(config)))
		for i, setting := range config {
			env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, setting.key), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, setting.value))
		}
	}
	if name == "git" {
		// No templates may seed local filters/includes; no monitor or external
		// attributes may influence materialization. Clone's own argv persists
		// the hooks, symlinks and submodule restrictions as well.
		config := []string{
			"-c", "init.templateDir=",
			"-c", "core.fsmonitor=false",
			"-c", "core.hooksPath=" + os.DevNull,
			"-c", "core.attributesFile=" + os.DevNull,
			"-c", "submodule.recurse=false"}
		args = append(config, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- only Tao-owned Git argv reaches this runner.
	cmd.Dir, cmd.Stdout, cmd.Stderr = cwd, stdout, stderr
	cmd.Env = env
	return cmd.Run()
}

// Git's environment can inject arbitrary config, templates, helpers, repository
// paths and object stores. Keep only explicit authentication inputs for clone;
// checkout and metadata probes receive none of them.
func isolatedGitEnv(cloning bool) []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			if !cloning || !slices.Contains([]string{
				"GIT_SSH", "GIT_SSH_COMMAND", "GIT_SSH_VARIANT", "GIT_ASKPASS",
				"GIT_SSL_CAINFO", "GIT_SSL_CAPATH",
			}, key) {
				continue
			}
		}
		env = append(env, entry)
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_ATTR_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1")
}

type gitSetting struct {
	key, value string
}

// Read trusted configuration as data, not execution policy. cwd is the reserved
// scratch parent outside any checkout. Includes are resolved by Git, but only
// this allowlist reaches clone, and nothing is carried forward to checkout.
func transportConfig(ctx context.Context, cwd string) ([]gitSetting, error) {
	env := isolatedGitEnv(false)
	// Restore the caller's system/global config locations only for this read.
	env = slices.DeleteFunc(env, func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return key == "GIT_CONFIG_NOSYSTEM" || key == "GIT_CONFIG_SYSTEM" || key == "GIT_CONFIG_GLOBAL"
	})
	for _, key := range []string{"GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_GLOBAL"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	cmd := exec.CommandContext(ctx, "git", "config", "--null", "--list")
	cmd.Dir, cmd.Env = cwd, env
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read Git transport configuration: %w", err)
	}
	var settings []gitSetting
	for _, entry := range strings.Split(string(output), "\x00") {
		key, value, hasValue := strings.Cut(entry, "\n")
		if !transportConfigKey(key) {
			continue
		}
		if !hasValue {
			value = "true"
		}
		settings = append(settings, gitSetting{key, value})
	}
	return settings, nil
}

func transportConfigKey(key string) bool {
	if key == "core.sshcommand" || key == "ssh.variant" {
		return true
	}
	// Preserve URL-scoped HTTP/credential settings and repeated values (such
	// as extraHeader and helper). Never admit URL rewrites or generic helpers.
	section, _, _ := strings.Cut(key, ".")
	field := key[strings.LastIndex(key, ".")+1:]
	switch section {
	case "credential":
		return slices.Contains([]string{"helper", "username", "usehttppath", "interactive"}, field)
	case "http":
		return slices.Contains([]string{
			"proxy", "proxyauthmethod", "extraheader", "cookiefile",
			"sslverify", "sslcainfo", "sslcapath", "sslcert", "sslkey",
			"sslcertpasswordprotected", "sslbackend", "version",
		}, field)
	default:
		return false
	}
}

// Fetch clones and hardens one snapshot without executing its contents. A
// reserved scratch directory is removed on failure, never retried more weakly.
func Fetch(ctx context.Context, opts Options) (result Result, retErr error) {
	if opts.DataHome == "" {
		return Result{}, errors.New("data home is required")
	}
	source, err := ValidateSourceURL(opts.Source.URL)
	if err != nil {
		return Result{}, err
	}
	home, err := filepath.Abs(opts.DataHome)
	if err != nil {
		return Result{}, err
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = MaxCloneBytes
	}
	if opts.MaxBytes < 0 {
		return Result{}, errors.New("clone cap must be positive")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Runner == nil {
		opts.Runner = defaultRunner
	}
	now := opts.Now()
	scratch := ScratchDir(home, source, now)
	parent := filepath.Dir(scratch)
	if err := refuseCheckoutParent(parent); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(parent, 0755); err != nil { // #nosec G301 -- public snapshots use standard traversable directories.
		return Result{}, err
	}
	// Reserve exclusively: timestamp collisions must not clobber old snapshots.
	if err := os.Mkdir(scratch, 0755); err != nil { // #nosec G301 -- public snapshots use standard traversable directories.
		return Result{}, fmt.Errorf("reserve snapshot %s: %w", scratch, err)
	}
	defer func() {
		if retErr != nil {
			if err := os.RemoveAll(scratch); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("cleanup failed; snapshot may remain at %s: %w", scratch, err))
			}
		}
	}()
	client := gitops.NewClient(parent, opts.Runner)
	if err := client.CloneShallowHardened(ctx, source.URL, scratch); err != nil {
		return Result{}, fmt.Errorf("clone snapshot: %w", err)
	}
	objectBytes, err := ObjectStoreBytes(scratch)
	if err != nil {
		return Result{}, fmt.Errorf("measure Git metadata: %w", err)
	}
	if objectBytes > opts.MaxBytes {
		return Result{}, fmt.Errorf("git metadata exceeds cap of %d bytes", opts.MaxBytes)
	}
	run := func(args ...string) (string, error) {
		var stdout bytes.Buffer
		if err := opts.Runner(ctx, scratch, "git", args, &stdout, io.Discard); err != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(stdout.String()), nil
	}
	if _, err := run("checkout", "--quiet"); err != nil {
		return Result{}, err
	}
	branch, err := run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return Result{}, err
	}
	commit, err := run("rev-parse", "HEAD")
	if err != nil {
		return Result{}, err
	}
	if branch == "" || commit == "" {
		return Result{}, errors.New("snapshot HEAD branch or commit is empty")
	}
	omitted, size, err := HardenSnapshot(scratch, opts.MaxBytes)
	if err != nil {
		return Result{}, fmt.Errorf("harden snapshot: %w", err)
	}
	return Result{
		SnapshotPath: scratch, DefaultBranch: branch, Commit: commit,
		DeclaredVersion: DeclaredVersion(scratch), CampaignTag: CampaignSlug(source, now),
		SizeBytes: size, Omitted: omitted,
	}, nil
}

// Resolve existing ancestors before creating anything, including a data home
// reached through a symlink. A worktree's .git file also marks a checkout.
func refuseCheckoutParent(parent string) error {
	existing := parent
	var missing []string
	for {
		_, err := os.Lstat(existing)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, filepath.Base(existing))
		next := filepath.Dir(existing)
		if next == existing {
			return err
		}
		existing = next
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	for dir := resolved; ; dir = filepath.Dir(dir) {
		_, err := os.Lstat(filepath.Join(dir, ".git"))
		if err == nil {
			return fmt.Errorf("scratch parent %s is inside Git checkout %s", parent, dir)
		}
		if !os.IsNotExist(err) {
			return err
		}
		if filepath.Dir(dir) == dir {
			return nil
		}
	}
}

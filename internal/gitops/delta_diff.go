package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

func deltaDiffArgs(args []string) []string {
	return append([]string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/"}, args...)
}

// DiffBoundedArgs drains the complete patch while retaining only its prefix.
// Callers supply revisions/options and separate literal paths with --.
func (c Client) DiffBoundedArgs(ctx context.Context, maxBytes int, args ...string) (string, bool, error) {
	if maxBytes <= 0 {
		return "", false, errors.New("bounded diff limit must be positive")
	}
	stdout := boundedWriter{limit: maxBytes}
	err := c.deltaDiff(ctx, &stdout, args)
	if err != nil {
		return "", stdout.truncated, err
	}
	return stdout.String(), stdout.truncated, nil
}

// DiffDigest hashes the complete patch stream without retaining stdout.
func (c Client) DiffDigest(ctx context.Context, args ...string) (string, error) {
	digest := sha256.New()
	if err := c.deltaDiff(ctx, digest, args); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (c Client) deltaDiff(ctx context.Context, stdout io.Writer, args []string) error {
	stderr := boundedWriter{limit: probeOutputLimit}
	argv := deltaDiffArgs(args)
	if err := c.readOnlyClient().git(ctx, argv, stdout, &stderr); err != nil {
		return commandError(argv, err, probeStderr(&stderr))
	}
	return nil
}

// VerifyCommit resolves only commit identities. A quiet missing ref is distinct
// from operational failures, malformed output, and cancellation.
func (c Client) VerifyCommit(ctx context.Context, ref string) (string, bool, error) {
	args := []string{"rev-parse", "--verify", "--quiet", "--end-of-options", ref + "^{commit}"}
	stdout := boundedWriter{limit: 128}
	stderr := boundedWriter{limit: probeOutputLimit}
	err := c.readOnlyClient().git(ctx, args, &stdout, &stderr)
	if ctx.Err() != nil {
		return "", false, ctx.Err()
	}
	if err != nil {
		var exit interface{ ExitCode() int }
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && errors.As(err, &exit) && exit.ExitCode() == 1 && stderr.String() == "" {
			return "", false, nil
		}
		return "", false, commandError(args, err, probeStderr(&stderr))
	}
	sha := strings.TrimSuffix(stdout.String(), "\n")
	decoded, decodeErr := hex.DecodeString(sha)
	if stdout.truncated || decodeErr != nil || (len(decoded) != 20 && len(decoded) != 32) || strings.ToLower(sha) != sha || strings.Trim(sha, "0") == "" {
		return "", false, errors.New("git rev-parse returned an invalid commit identity")
	}
	return sha, true, nil
}

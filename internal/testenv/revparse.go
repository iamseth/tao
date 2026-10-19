package testenv

import (
	"context"
	"io"
)

// SplitBatchedRevParse lets a single-query Git fake answer a batched
// rev-parse the way Git does: one output line per query, in order. Wrap fakes
// that recognize only "rev-parse <query>" commands.
func SplitBatchedRevParse(runner func(context.Context, string, string, []string, io.Writer, io.Writer) error) func(context.Context, string, string, []string, io.Writer, io.Writer) error {
	return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		prefix, rest := []string{}, args
		if len(rest) >= 2 && rest[0] == "-C" {
			prefix, rest = rest[:2], rest[2:]
		}
		if name != "git" || len(rest) < 3 || rest[0] != "rev-parse" {
			return runner(ctx, cwd, name, args, stdout, stderr)
		}
		for _, query := range rest[1:] {
			single := append(append([]string{}, prefix...), "rev-parse", query)
			if err := runner(ctx, cwd, name, single, stdout, stderr); err != nil {
				return err
			}
		}
		return nil
	}
}

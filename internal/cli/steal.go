package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/steal"
	"github.com/iamseth/tao/internal/taodata"
)

var stealCommand = commandMetadata{
	name:                  "steal",
	minPrefix:             "ste",
	usageLines:            []string{"steal fetch <git-url>"},
	completionDescription: "Fetch a foreign repository snapshot for read-only scouting",
	long:                  "Fetch a hardened shallow snapshot of a foreign repository into Tao's data home for read-only scouting. Snapshot content is untrusted and is never executed.",
	examples:              "  tao steal fetch https://example.com/owner/repo.git",
	subcommands: []commandSubcommand{
		{name: "fetch", description: "Perform the hardened shallow clone into Tao's data home"},
	},
	execute: func(c commandContext) error {
		return c.app.steal(c.ctx, c.args)
	},
}

var stealFetch = steal.Fetch

func (a App) steal(ctx context.Context, args []string) error {
	if len(args) != 2 || args[0] != "fetch" {
		return errors.New("usage: tao steal fetch <git-url>")
	}
	source, err := steal.ValidateSourceURL(args[1])
	if err != nil {
		return err
	}
	home := taodata.DataHome()
	if err := checkStealScratch(ctx, filepath.Join(home, "steal")); err != nil {
		return err
	}
	result, err := stealFetch(ctx, steal.Options{DataHome: home, Source: source, Now: time.Now, MaxBytes: steal.MaxCloneBytes})
	if err != nil {
		return err
	}
	// Keep repository-controlled values from forging metadata lines or terminal
	// controls. The scouting prompt decodes these Go string literals once.
	quote := strconv.QuoteToASCII
	lines := []string{
		"Snapshot: " + quote(result.SnapshotPath),
		"Source: " + quote(source.URL),
		"Host: " + quote(source.Host),
		"Default branch: " + quote(result.DefaultBranch),
		"Commit: " + quote(result.Commit),
		"Declared version: " + quote(emptyDash(result.DeclaredVersion)),
		"Campaign tag: " + quote(result.CampaignTag),
		fmt.Sprintf("Size bytes: %d", result.SizeBytes),
		fmt.Sprintf("Omitted: %d", len(result.Omitted)),
	}
	for _, omission := range result.Omitted {
		lines = append(lines, "  "+quote(omission.Path)+"  "+quote(omission.Reason))
	}
	lines = append(lines, "Removal: "+quote("rm -rf '"+strings.ReplaceAll(result.SnapshotPath, "'", `'"'"'`)+"'"))
	return writeLines(a.Out, lines...)
}

func checkStealScratch(ctx context.Context, parent string) error {
	resolved, err := resolveStealPath(parent)
	if err != nil {
		return fmt.Errorf("resolve scratch parent: %w", err)
	}
	repos, err := taodata.NewRegistry("").ListRepos()
	if err != nil {
		return err
	}
	var roots []string
	for _, repo := range repos {
		if repo.Root != "" {
			roots = append(roots, repo.Root)
		}
	}
	if root, err := gitops.NewClient("", nil).RevParse(ctx, "--show-toplevel"); err == nil {
		roots = append(roots, strings.TrimSpace(root))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, root := range roots {
		canonical, err := resolveStealPath(root)
		if err != nil {
			return fmt.Errorf("resolve repository root %s: %w", root, err)
		}
		relative, err := filepath.Rel(canonical, resolved)
		if err != nil {
			return err
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("scratch parent %s is inside repository checkout %s", resolved, canonical)
		}
	}
	return nil
}

// Resolve the deepest existing ancestor without creating scratch directories.
// Lstat distinguishes absent descendants from dangling or inaccessible symlinks,
// which must fail closed rather than bypass checkout confinement.
func resolveStealPath(path string) (string, error) {
	candidate, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var missing []string
	for {
		_, err := os.Lstat(candidate)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) || filepath.Dir(candidate) == candidate {
			return "", err
		}
		missing = append(missing, filepath.Base(candidate))
		candidate = filepath.Dir(candidate)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	return resolved, nil
}

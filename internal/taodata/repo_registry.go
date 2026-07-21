package taodata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/atomicfile"
	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/gitops"
)

const RepoSchema = "tao.repo.v1"

// Repo describes one registered source repository under Tao's data home.
type Repo struct {
	Schema    string `json:"schema"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Root      string `json:"root"`
	Branch    string `json:"branch,omitempty"`
	RemoteURL string `json:"remote_url,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

// PlanAllocation describes a centralized plan directory allocated for a repo.
type PlanAllocation struct {
	ID         string `json:"id"`
	Dir        string `json:"dir"`
	BaseCommit string `json:"base_commit,omitempty"`
}

type Registry struct {
	DataHome string
	Runner   commandrunner.Runner
	Now      func() time.Time
}

func NewRegistry(dataHome string) Registry {
	if dataHome == "" {
		dataHome = DataHome()
	}
	return Registry{DataHome: dataHome, Runner: commandrunner.DefaultLocal, Now: time.Now}
}

func (r Registry) RegisterCurrent(ctx context.Context) (Repo, error) {
	root, err := gitops.NewClient("", r.commandRunner()).RevParse(ctx, "--show-toplevel")
	if err != nil {
		return Repo{}, fmt.Errorf("resolve git root: %w", err)
	}
	root, err = filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return Repo{}, fmt.Errorf("canonicalize git root: %w", err)
	}
	client := gitops.NewClient(root, r.commandRunner())
	branch, _ := client.CurrentBranch(ctx)
	remote, _ := client.RemoteURL(ctx)
	repo := Repo{
		Schema:    RepoSchema,
		ID:        RepoID(root),
		Name:      filepath.Base(root),
		Root:      root,
		Branch:    strings.TrimSpace(branch),
		RemoteURL: strings.TrimSpace(remote),
		UpdatedAt: r.now().UTC().Format(time.RFC3339),
	}
	if err := r.WriteRepo(repo); err != nil {
		return Repo{}, err
	}
	return repo, nil
}

func (r Registry) Current(ctx context.Context) (Repo, error) {
	root, err := gitops.NewClient("", r.commandRunner()).RevParse(ctx, "--show-toplevel")
	if err != nil {
		return Repo{}, fmt.Errorf("resolve git root: %w", err)
	}
	root, err = filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return Repo{}, fmt.Errorf("canonicalize git root: %w", err)
	}
	return r.RepoForRoot(root)
}

func (r Registry) RepoForRoot(root string) (Repo, error) {
	root, err := filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return Repo{}, fmt.Errorf("canonicalize git root: %w", err)
	}
	repo := Repo{ID: RepoID(root), Name: filepath.Base(root), Root: root}
	stored, err := r.ReadRepo(repo.ID)
	if err == nil {
		return stored, nil
	}
	if !os.IsNotExist(err) {
		return Repo{}, err
	}
	return repo, nil
}

func (r Registry) ReadRepo(id string) (Repo, error) {
	content, err := os.ReadFile(filepath.Join(r.DataHome, "repos", id, "repo.json")) //nolint:gosec // G304: path built from registry data home and repo id
	if err != nil {
		return Repo{}, err
	}
	var repo Repo
	if err := json.Unmarshal(content, &repo); err != nil {
		return Repo{}, fmt.Errorf("read repo metadata: %w", err)
	}
	return repo, nil
}

func (r Registry) ListRepos() ([]Repo, error) {
	entries, err := r.repoEntries()
	if err != nil {
		return nil, err
	}
	var repos []Repo
	for _, entry := range entries {
		repo, err := r.ReadRepo(entry.Name())
		if err != nil {
			continue
		}
		repos = append(repos, repo)
	}
	return repos, nil
}

// RepoCatalogEntry exposes registered repo metadata with derived, non-destructive status.
type RepoCatalogEntry struct {
	Repo          Repo
	PlanCount     int
	Health        RepoHealth
	MetadataError error
}

func (r Registry) Catalog(ctx context.Context, checker RepoHealthChecker) ([]RepoCatalogEntry, error) {
	entries, err := r.repoEntries()
	if err != nil {
		return nil, err
	}
	catalog := make([]RepoCatalogEntry, 0, len(entries))
	for _, entry := range entries {
		repo, err := r.ReadRepo(entry.Name())
		if err != nil {
			catalog = append(catalog, RepoCatalogEntry{
				Repo:          Repo{ID: entry.Name()},
				Health:        metadataErrorHealth(err),
				MetadataError: err,
			})
			continue
		}
		catalog = append(catalog, RepoCatalogEntry{
			Repo:      repo,
			PlanCount: r.planCount(repo),
			Health:    checker.Check(ctx, repo),
		})
	}
	return catalog, nil
}

func (r Registry) repoEntries() ([]os.DirEntry, error) {
	root := filepath.Join(r.DataHome, "repos")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var filtered []os.DirEntry
	for _, entry := range entries {
		if entry.IsDir() {
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
}

func (r Registry) planCount(repo Repo) int {
	entries, err := os.ReadDir(r.PlansDir(repo))
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			count++
		}
	}
	return count
}

func (r Registry) PlansDir(repo Repo) string {
	return filepath.Join(r.DataHome, "repos", repo.ID, "plans")
}

// NotesDir returns the repository-owned note store. It intentionally does not
// fall back to the retired global notes directory.
func (r Registry) NotesDir(repo Repo) string {
	return filepath.Join(r.DataHome, "repos", repo.ID, "notes")
}

func (r Registry) QueuePath(repo Repo) string {
	return filepath.Join(r.DataHome, "repos", repo.ID, "queue.json")
}

func (r Registry) QueueLogPath(repo Repo) string {
	return filepath.Join(r.DataHome, "repos", repo.ID, "queue.jsonl")
}

// MergeBatchesDir returns the repository-owned merge batch store. Batch
// artifacts deliberately live beside plans, never inside the plans directory.
func (r Registry) MergeBatchesDir(repo Repo) string {
	return filepath.Join(r.DataHome, "repos", repo.ID, "merge-batches")
}

func (r Registry) MergeBatchDir(repo Repo, batchID string) string {
	return filepath.Join(r.MergeBatchesDir(repo), batchID)
}

func (r Registry) MergeBatchStatePath(repo Repo, batchID string) string {
	return filepath.Join(r.MergeBatchDir(repo, batchID), "state.json")
}

func (r Registry) MergeBatchLogPath(repo Repo, batchID string) string {
	return filepath.Join(r.MergeBatchDir(repo, batchID), "transitions.jsonl")
}

func (r Registry) ActiveMergeBatchPath(repo Repo) string {
	return filepath.Join(r.MergeBatchesDir(repo), "active.json")
}

func (r Registry) WriteRepo(repo Repo) error {
	dir := filepath.Join(r.DataHome, "repos", repo.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create repo registry dir: %w", err)
	}
	content, err := json.MarshalIndent(repo, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if err := atomicfile.Write(filepath.Join(dir, "repo.json"), content, atomicfile.Options{Perm: 0o600}); err != nil {
		return fmt.Errorf("write repo metadata: %w", err)
	}
	return nil
}

func (r Registry) AllocatePlan(repo Repo, slug string) (PlanAllocation, error) {
	clean := cleanSlug(slug)
	if clean == "" {
		return PlanAllocation{}, fmt.Errorf("slug is required")
	}
	baseID := r.now().UTC().Format("20060102-150405") + "-" + clean
	id := baseID
	plansRoot := filepath.Join(r.DataHome, "repos", repo.ID, "plans")
	if err := os.MkdirAll(plansRoot, 0o700); err != nil {
		return PlanAllocation{}, fmt.Errorf("create plans root: %w", err)
	}
	for i := 2; ; i++ {
		dir := filepath.Join(plansRoot, id)
		err := os.Mkdir(dir, 0o700)
		if err == nil {
			return PlanAllocation{ID: id, Dir: dir}, nil
		}
		if !os.IsExist(err) {
			return PlanAllocation{}, fmt.Errorf("create plan dir: %w", err)
		}
		id = fmt.Sprintf("%s-%d", baseID, i)
	}
}

func RepoID(canonicalRoot string) string {
	name := cleanSlug(filepath.Base(canonicalRoot))
	if name == "" {
		name = "repo"
	}
	sum := sha256.Sum256([]byte(filepath.Clean(canonicalRoot)))
	return name + "-" + hex.EncodeToString(sum[:])[:12]
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug lowercases value, collapses runs of non-alphanumeric characters into
// single dashes, trims leading/trailing dashes, and caps the result at max
// characters (re-trimming any dash exposed by the cut). A non-positive max
// leaves the length unbounded.
func Slug(value string, max int) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = slugRe.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if max > 0 && len(value) > max {
		value = strings.Trim(value[:max], "-")
	}
	return value
}

func cleanSlug(value string) string {
	return Slug(value, 80)
}

func (r Registry) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r Registry) commandRunner() commandrunner.Runner {
	if r.Runner != nil {
		return r.Runner
	}
	return commandrunner.DefaultLocal
}

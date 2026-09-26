package uistate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iamseth/tao/internal/atomicfile"
)

// Filters is the persisted dashboard filter configuration, independent of TUI state.
type Filters struct {
	Version      int      `json:"version"`
	Enabled      bool     `json:"enabled"`
	Repositories []string `json:"repositories"`
	Statuses     []string `json:"statuses"`
	Tags         []string `json:"tags"`
}

// Store persists dashboard preferences directly under Tao's data home.
type Store struct {
	DataHome string
}

func (s Store) Path() string {
	return filepath.Join(s.DataHome, "ui-filters.json")
}

// Load returns defaults for a missing file. Callers should treat errors as
// best-effort warnings, not startup failures.
func (s Store) Load() (Filters, error) {
	path := s.Path()
	content, err := os.ReadFile(path) // #nosec G304 -- the caller supplies Tao's data home.
	if errors.Is(err, os.ErrNotExist) {
		return Filters{}, nil
	}
	if err != nil {
		return Filters{}, fmt.Errorf("read UI filters %q: %w", path, err)
	}
	var filters Filters
	if err := json.Unmarshal(content, &filters); err != nil {
		return Filters{}, fmt.Errorf("decode UI filters %q: %w", path, err)
	}
	return filters, nil
}

// Save atomically replaces the configuration with private file permissions.
func (s Store) Save(filters Filters) error {
	content, err := json.Marshal(filters)
	if err != nil {
		return fmt.Errorf("encode UI filters: %w", err)
	}
	path := s.Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create UI filters directory %q: %w", filepath.Dir(path), err)
	}
	if err := atomicfile.Write(path, append(content, '\n'), atomicfile.Options{Perm: 0o600}); err != nil {
		return fmt.Errorf("write UI filters %q: %w", path, err)
	}
	return nil
}

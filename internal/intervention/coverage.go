package intervention

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"unicode/utf8"
)

const coverageFile = "intervention-coverage.json"
const coverageSchema = "tao.intervention.coverage.v1"
const maxCoverageBytes = 4096

type coverage struct {
	Schema  string `json:"schema"`
	Version int    `json:"version"`
	PlanID  string `json:"plan_id"`
}

func validPlanID(id string) bool { return len(id) > 0 && len(id) <= 512 && utf8.ValidString(id) }

// WriteCoverage exclusively publishes a complete marker in an existing directory.
// Callers must treat errors as diagnostics; this does not backfill history.
func WriteCoverage(dir, planID string) error {
	if !validPlanID(planID) {
		return errors.New("invalid coverage identity")
	}
	data, err := json.Marshal(coverage{coverageSchema, 1, planID})
	if err != nil {
		return err
	}
	if len(data) > maxCoverageBytes {
		return errors.New("coverage exceeds bound")
	}
	f, err := os.CreateTemp(dir, ".intervention-coverage-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Linking a private complete inode publishes atomically and never replaces
	// an existing destination, including a dangling symlink.
	return os.Link(f.Name(), filepath.Join(dir, coverageFile))
}

// ReadCoverage returns false for incomplete evidence. Only absence is silent;
// other errors are diagnostic and must never affect lifecycle decisions.
func ReadCoverage(dir, planID string) (bool, error) {
	if !validPlanID(planID) {
		return false, errors.New("invalid coverage identity")
	}
	path := filepath.Join(dir, coverageFile)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxCoverageBytes {
		return false, errors.New("invalid coverage file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) // #nosec G304 -- fixed marker basename in caller-selected storage; no-follow, regular-file and identity checks bound reads.
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return false, errors.New("coverage file changed")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCoverageBytes+1))
	if err != nil {
		return false, err
	}
	if len(data) > maxCoverageBytes {
		return false, errors.New("coverage exceeds bound")
	}
	var marker coverage
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&marker); err != nil {
		return false, fmt.Errorf("decode coverage: %w", err)
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return false, errors.New("trailing coverage data")
	}
	if marker.Schema != coverageSchema || marker.Version != 1 || marker.PlanID != planID {
		return false, errors.New("unsupported coverage or identity mismatch")
	}
	return true, nil
}

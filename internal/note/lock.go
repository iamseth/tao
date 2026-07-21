package note

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	defaultStaleLockAge = 24 * time.Hour
	mutationLockPoll    = 10 * time.Millisecond
)

type PromotionLockOwner struct {
	Owner     string    `json:"owner"`
	PID       int       `json:"pid"`
	CreatedAt time.Time `json:"created_at"`
	Token     string    `json:"token"`
}

// PromotionLockError reports the current lock holder.
type PromotionLockError struct {
	NoteID string
	Path   string
	Holder PromotionLockOwner
}

func (e *PromotionLockError) Error() string {
	holder := strings.TrimSpace(e.Holder.Owner)
	if holder == "" {
		holder = "unknown owner"
	}
	if e.Holder.PID > 0 {
		holder = fmt.Sprintf("%s (pid %d)", holder, e.Holder.PID)
	}
	return fmt.Sprintf("note %s promotion is already locked by %s", e.NoteID, holder)
}
func (e *PromotionLockError) Is(target error) bool { return target == ErrPromotionLocked }

// PromotionLocker coordinates promotion across Tao processes.
type PromotionLocker struct {
	Dir         string
	Now         func() time.Time
	PID         func() int
	Token       func() string
	ProcessLive func(int) bool
	StaleAfter  time.Duration
}

type PromotionLock struct {
	path    string
	owner   PromotionLockOwner
	content []byte
}

type observedPromotionLock struct {
	owner   PromotionLockOwner
	info    os.FileInfo
	content []byte
}

type mutationLock struct {
	file *os.File
}

// acquireMutationLock serializes a note's short read-modify-replace sequence
// across repository instances and processes. Promotion's longer-lived lock is
// intentionally separate so destination creation does not block ordinary edits.
func acquireMutationLock(ctx context.Context, dir, noteID string) (*mutationLock, error) {
	path := filepath.Join(dir, "."+noteID+".mutation.lock")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- safe note id and configured note directory.
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &mutationLock{file: file}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("lock note mutation: %w", err)
		}
		timer := time.NewTimer(mutationLockPoll)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *mutationLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	return errors.Join(unlockErr, closeErr)
}

func NewPromotionLocker(dir string) *PromotionLocker { return &PromotionLocker{Dir: dir} }

func (l *PromotionLocker) Acquire(ctx context.Context, noteID, owner string) (*PromotionLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	noteID, err := validateLookupID(noteID)
	if err != nil {
		return nil, err
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, errors.New("promotion lock owner is required")
	}
	if err := l.ensureDir(); err != nil {
		return nil, err
	}
	metadata := PromotionLockOwner{Owner: owner, PID: l.pid(), CreatedAt: l.now().UTC(), Token: l.token()}
	content, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	content = append(content, '\n')
	path := filepath.Join(l.Dir, "."+noteID+".promotion.lock")
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- safe note id and configured note directory.
		if createErr == nil {
			if _, writeErr := file.Write(content); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, writeErr
			}
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(path)
				return nil, closeErr
			}
			return &PromotionLock{path: path, owner: metadata, content: content}, nil
		}
		if !os.IsExist(createErr) {
			return nil, createErr
		}
		observed, readErr := readPromotionLock(path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return nil, readErr
		}
		if !l.stale(observed.owner, observed.info) {
			return nil, &PromotionLockError{NoteID: noteID, Path: path, Holder: observed.owner}
		}
		current, readErr := readPromotionLock(path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return nil, readErr
		}
		if !os.SameFile(observed.info, current.info) || !bytes.Equal(observed.content, current.content) {
			continue
		}
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return nil, fmt.Errorf("reclaim stale promotion lock: %w", removeErr)
		}
	}
}

// Release removes the lock only if the on-disk owner token still belongs to
// this acquisition. A replaced lock is never removed.
func (l *PromotionLock) Release() error {
	if l == nil || l.path == "" {
		return nil
	}
	content, err := os.ReadFile(l.path) // #nosec G304 -- path was created by PromotionLocker.
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(content, l.content) {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (l *PromotionLock) Owner() PromotionLockOwner {
	if l == nil {
		return PromotionLockOwner{}
	}
	return l.owner
}

func (l *PromotionLocker) ensureDir() error {
	if strings.TrimSpace(l.Dir) == "" {
		return errors.New("note directory is required")
	}
	parent := filepath.Dir(l.Dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(parent, 0o700); err != nil { //nolint:gosec // G302: directories require owner search permission.
		return err
	}
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(l.Dir, 0o700) //nolint:gosec // G302: directories require owner search permission.
}
func (l *PromotionLocker) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}
func (l *PromotionLocker) pid() int {
	if l.PID != nil {
		return l.PID()
	}
	return os.Getpid()
}
func (l *PromotionLocker) token() string {
	if l.Token != nil {
		return l.Token()
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
}
func (l *PromotionLocker) stale(owner PromotionLockOwner, info os.FileInfo) bool {
	if owner.PID > 0 {
		live := defaultProcessLive
		if l.ProcessLive != nil {
			live = l.ProcessLive
		}
		// A known live process is never reclaimed merely due to age.
		return !live(owner.PID)
	}
	age := defaultStaleLockAge
	if l.StaleAfter != 0 {
		age = l.StaleAfter
	}
	return age > 0 && l.now().Sub(info.ModTime()) >= age
}
func readPromotionLock(path string) (observedPromotionLock, error) {
	info, err := os.Stat(path)
	if err != nil {
		return observedPromotionLock{}, err
	}
	content, err := os.ReadFile(path) // #nosec G304 -- configured note lock path.
	if err != nil {
		return observedPromotionLock{}, err
	}
	observed := observedPromotionLock{info: info, content: content}
	if err := json.Unmarshal(content, &observed.owner); err != nil {
		// Malformed ownership remains conservatively held until the age limit.
		return observed, nil //nolint:nilerr // Invalid metadata is represented as an unknown owner.
	}
	return observed, nil
}
func defaultProcessLive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer func() { _ = process.Release() }()
	err = process.Signal(syscall.Signal(0))
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true
	}
	return !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH)
}

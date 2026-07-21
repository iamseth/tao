// Package herdr contains Tao's best-effort Herdr status reporter.
package herdr

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

const (
	envHerdr      = "HERDR_ENV"
	envSocketPath = "HERDR_SOCKET_PATH"
	envPaneID     = "HERDR_PANE_ID"

	herdrAgent  = "tao"
	herdrSource = "tao"

	reportTimeout = 200 * time.Millisecond
)

// Reporter sends newline-framed Herdr API reports when Tao is running inside
// a Herdr pane. A disabled reporter is a no-op, and enabled reporters swallow
// all socket and write failures.
type Reporter struct {
	socketPath string
	paneID     string
	enabled    bool
	requests   atomic.Uint64

	mu                 sync.Mutex
	nextTrackID        uint64
	activeTracks       map[uint64]string
	activeTrackOrder   []uint64
	blockedDuringTrack bool
}

// New returns a reporter configured from Herdr-injected environment variables.
// If Tao is not running inside Herdr, the returned reporter is disabled.
func New() *Reporter {
	socketPath := os.Getenv(envSocketPath)
	paneID := os.Getenv(envPaneID)
	enabled := os.Getenv(envHerdr) != "" && socketPath != "" && paneID != ""
	return &Reporter{socketPath: socketPath, paneID: paneID, enabled: enabled}
}

// Enabled reports whether Herdr environment metadata was present at creation.
func (r *Reporter) Enabled() bool {
	return r != nil && r.enabled
}

// Working reports that Tao is actively running a plan.
func (r *Reporter) Working(status string) {
	r.report("working", status)
}

// Idle reports that Tao has settled without an error.
func (r *Reporter) Idle() {
	r.report("idle", "")
}

// Blocked reports that Tao has settled with an error or panic.
func (r *Reporter) Blocked() {
	r.report("blocked", "")
}

// Track reports working, runs fn, and settles to idle on success or blocked on
// error. Concurrent Track calls share the pane: if one run exits while another
// is active, Track re-reports an active run instead of settling the pane. Panics
// are treated as blocked exits before re-panicking, with blocked reporting
// deferred while other tracked runs remain active.
func (r *Reporter) Track(status string, fn func() error) (err error) {
	if !r.Enabled() {
		return fn()
	}

	trackID := r.startTrack(status)
	defer func() {
		if recovered := recover(); recovered != nil {
			r.finishTrack(trackID, true)
			panic(recovered)
		}
		r.finishTrack(trackID, err != nil)
	}()
	return fn()
}

func (r *Reporter) startTrack(status string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.activeTracks) == 0 {
		r.blockedDuringTrack = false
	}
	if r.activeTracks == nil {
		r.activeTracks = make(map[uint64]string)
	}

	r.nextTrackID++
	trackID := r.nextTrackID
	r.activeTracks[trackID] = status
	r.activeTrackOrder = append(r.activeTrackOrder, trackID)
	r.sendReport("working", status)
	return trackID
}

func (r *Reporter) finishTrack(trackID uint64, blocked bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if blocked {
		r.blockedDuringTrack = true
	}
	if _, ok := r.activeTracks[trackID]; ok {
		delete(r.activeTracks, trackID)
		r.removeActiveTrack(trackID)
	}
	if status, ok := r.lastActiveTrackStatus(); ok {
		r.sendReport("working", status)
		return
	}
	if r.blockedDuringTrack {
		r.blockedDuringTrack = false
		r.sendReport("blocked", "")
		return
	}
	r.sendReport("idle", "")
}

func (r *Reporter) removeActiveTrack(trackID uint64) {
	for i, activeTrackID := range r.activeTrackOrder {
		if activeTrackID == trackID {
			r.activeTrackOrder = append(r.activeTrackOrder[:i], r.activeTrackOrder[i+1:]...)
			return
		}
	}
}

func (r *Reporter) lastActiveTrackStatus() (string, bool) {
	for _, trackID := range slices.Backward(r.activeTrackOrder) {
		status, ok := r.activeTracks[trackID]
		if ok {
			return status, true
		}
	}
	return "", false
}

func (r *Reporter) report(state string, status string) {
	if !r.Enabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sendReport(state, status)
}

func (r *Reporter) sendReport(state string, status string) {
	payload, err := json.Marshal(reportRequest{
		ID:     r.nextRequestID(),
		Method: "pane.report_agent",
		Params: reportParams{
			PaneID:       r.paneID,
			Source:       herdrSource,
			Agent:        herdrAgent,
			State:        state,
			CustomStatus: status,
		},
	})
	if err != nil {
		return
	}
	payload = append(payload, '\n')

	dialer := net.Dialer{Timeout: reportTimeout}
	conn, err := dialer.Dial("unix", r.socketPath)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(reportTimeout))
	_, _ = conn.Write(payload)
}

func (r *Reporter) nextRequestID() string {
	return fmt.Sprintf("tao-%d", r.requests.Add(1))
}

type reportRequest struct {
	ID     string       `json:"id"`
	Method string       `json:"method"`
	Params reportParams `json:"params"`
}

type reportParams struct {
	PaneID       string `json:"pane_id"`
	Source       string `json:"source"`
	Agent        string `json:"agent"`
	State        string `json:"state"`
	CustomStatus string `json:"custom_status"`
}

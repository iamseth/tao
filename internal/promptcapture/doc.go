// Package promptcapture owns the local-only prompt capture file contract.
// Capture is best-effort: callers must not let a write failure alter a session.
// Captures are never lifecycle or recovery evidence and are never read by plan
// loaders. Files share their parent plan or batch directory's retention.
package promptcapture

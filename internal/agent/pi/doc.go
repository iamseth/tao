// Package pi owns low-level Pi RPC process mechanics for Tao integrations.
//
// It starts a fresh `pi --mode rpc` process for each client session, speaks the
// JSONL protocol, and exposes best-effort session metadata. Higher-level
// packages own Tao plan behavior, reviews, and telemetry decisions.
package pi

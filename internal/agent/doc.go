// Package agent defines the canonical agent-runtime contract used by the run
// layer to drive supported agent providers.
//
// The Runtime interface, the neutral Session/SessionResult/Metrics types, and
// the PermissionMode policy form a single seam so that every agent-kind dispatch
// becomes a registry lookup rather than a transport-shaped switch. Adapters in
// this package wrap the leaf pi.Client and claude.Client, normalizing their
// divergent request and metrics types onto the neutral contract.
//
// Import direction is one-way: this root may import the leaf clients
// (internal/agent/pi, internal/agent/claude) and internal/runtimeconfig for
// AgentKind, but it must never import internal/plan, internal/run, or
// internal/cli. Those higher layers depend on this package, not the reverse,
// which keeps the clients leaf packages and avoids import cycles.
package agent

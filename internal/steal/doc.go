// Package steal owns the hardened scouting fetch used by /tao-steal.
// Snapshots are untrusted data: fetching, probing, and hardening never execute
// repository content. Git runs without inherited Git configuration or repository
// environment overrides. Clone alone receives allowlisted system/global HTTP,
// credential and SSH settings (including trusted authentication helpers), plus
// explicit SSH/CA/askpass environment inputs; normal proxy and SSH-agent
// environment remains available. URL rewrites and environment-injected config
// are not supported. Templates, filesystem monitors and external attributes are
// disabled, and checkout receives no transport configuration or filter drivers,
// so LFS pointers remain source text rather than fetching their referenced data.
// Prompt reading budgets are separate from the clone cap.
package steal

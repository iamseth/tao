// Package metrics defines the provider-neutral typed measurement model for one
// agent session. Provider adapters populate the measurements they support and
// leave unavailable values at zero for higher layers to interpret and persist.
// Metrics are best-effort observations only; they do not establish completion,
// recovery, or any other lifecycle authority.
package metrics

// Package taodata resolves Tao's centralized, inspectable runtime data home and
// the repository registry and health that live under it.
//
// It is the single source of truth for data-home resolution precedence
// (TAO_DATA_HOME, then XDG_DATA_HOME, then the user home) so callers never
// hardcode paths. It also owns the registered-repository catalog and the
// RepoHealthChecker used to gate execution against a repo root; plan artifacts
// stored beneath the data home remain owned by internal/plan.
package taodata

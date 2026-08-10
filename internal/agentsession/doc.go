// Package agentsession runs one bounded, provider-neutral agent session.
//
// It owns provider descriptor policy, wall-clock timeout application, progress
// routing, metric-warning classification, and control-checkout leak detection.
// Callers retain ownership of domain persistence, lifecycle, and budgets.
package agentsession

// Package plannerroute owns planner routing policy, assignment, and ledger
// models. Route records are local, bounded, non-authoritative observations;
// they are never lifecycle or recovery evidence. Routing data belongs outside
// plan artifacts and events.
//
// This package depends on runtimeconfig for runtime kinds. runtimeconfig must
// not import plannerroute.
package plannerroute

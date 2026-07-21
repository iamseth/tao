// Package planning owns the two planning seams used by note promotion.
//
// FileRepository.CreateSession persists the repo-scoped source record used by
// tao note plan. Service.GeneratePlan synchronously allocates, generates, and
// validates an executable plan for tao note run. Session values carry the source
// provenance and prompt transcript needed by either path.
//
// Planning delegates executable plan artifacts and lifecycle state to
// internal/plan; it owns only session persistence and provider-neutral plan
// generation.
package planning

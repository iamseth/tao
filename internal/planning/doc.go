// Package planning owns provider-neutral plan generation for tao note run and
// read-only compatibility access to historical planning-session records.
//
// New note-aware planning starts from /tao-plan note:<id>; after /tao-slice
// validates the resulting normal plan, the note is archived with that plan
// link. Tao no longer creates planning-session records, but Session models and
// FileRepository decode paths remain for historical provenance.
//
// Planning delegates executable plan artifacts and lifecycle state to
// internal/plan.
package planning

// Package rework owns the policy for turning review findings into bounded plan
// rework. Its ordinary authority arm requires a completed, changes-requested Tao
// review with actionable findings;
// its separate pull-request arm requires current approved PR completion and
// validated unresolved change threads. Forced reopening remains an explicit
// caller choice and cannot substitute for pull-request authority.
//
// The automatic driver bounds reopen-and-run cycles by a persisted baseline and
// attempt cap, and stops on equivalent findings or recurring finding files rather
// than treating another round as progress. Restarting after a durable stop must
// be explicitly authorized and establishes a fresh bounded window.
//
// Rework prepares generated slices and typed round or stop evidence, but does not
// choose filesystem persistence primitives. Mutations cross the narrow Record
// interfaces and are settled atomically by the plan-owned persistence boundary.
package rework

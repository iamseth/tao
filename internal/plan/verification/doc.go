// Package verification implements command-level verification analysis for plan facades.
//
// It intentionally depends on narrow value types instead of internal/plan artifact
// models so the plan package keeps ownership of on-disk schemas and lifecycle
// state while delegating shell, cwd, path, and failure-classification details.
//
// AnalyzeCommand, ClassifyRun, and ClassifyFailure are advisory; their suggested
// commands must never authorize execution. MechanicalCorrection is a separate,
// fail-closed producer of command/cwd candidates, not evidence of a passed gate.
// It currently supports only go test after a no-test-files diagnostic, with an
// explicit package CWD or a single leading "cd DIR && " (relative to the supplied
// CWD, or repository root). It removes that cd prefix and returns the canonical
// package CWD. Exactly one missing target must repeat that CWD's repository-relative
// prefix; the corrected target must exist inside the canonical repository root.
// Supported targets are .go files or explicit ./directory[/...] selectors. Bare
// import-path corrections, multiple corrections, and escaping symlinks are refused.
//
// The command grammar is unquoted ASCII words separated by single spaces. Words
// contain only letters, digits, and _./-=:,+@%^; && is allowed only in the cd prefix.
// Known boolean flags are -race, -short, -v, -failfast, -cover, -fullpath, -json, -x,
// and -work (optionally =true or =false). Known value flags are -run, -skip, -bench,
// -benchtime, -count, -cpu, -parallel, -timeout, -shuffle, and -tags, with either a
// separate value or =value. Flag values are never correction targets. Every token
// other than the one target is preserved; no tool, flag, filter, or target is
// dropped. Unknown tools/flags, wrappers, passthrough arguments, quotes, escapes,
// substitutions, globbing, redirection, pipelines, other control flow, and ambiguous
// whitespace are unsupported. This intentionally rejects valid but unproven shell
// commands rather than claiming general shell equivalence. Callers still own
// executing the candidate and observing its result; this package never retries.
package verification

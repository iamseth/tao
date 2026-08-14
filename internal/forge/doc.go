// Package forge owns interactions with repository hosting services.
//
// Pull-request lifecycle orchestration remains with callers; this package owns
// GitHub CLI execution, repository identity checks, response parsing, labels,
// assignment, and metadata repair.
package forge

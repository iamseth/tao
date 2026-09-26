// Package forge owns interactions with repository hosting services.
//
// Pull-request lifecycle orchestration remains with callers; this package owns
// GitHub CLI execution, repository identity checks, response parsing, labels,
// assignment, metadata repair, and review-thread reading.
package forge

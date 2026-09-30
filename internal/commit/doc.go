// Package commit owns strict commit-proposal decoding and central validation of
// the scoped Conventional Commit subject and non-empty what/why body.
//
// Tao appends trusted Tao-* trailers; proposals may not supply them. Invalid
// proposals stop before intent or Git mutation, with no deterministic or title
// fallback. Historical intents and messages remain readable verbatim.
package commit

// Package note owns repository-scoped backlog note artifacts and their
// lifecycle. Plan-linked archives are terminal and preserve any historical
// planning-session provenance; ordinary manual archives remain reversible.
// Stores are explicitly rooted beneath a registered repository; this package
// never discovers or reads the retired global note store. Readiness evaluation
// is advisory planning context only and is never lifecycle or promotion
// authority.
package note

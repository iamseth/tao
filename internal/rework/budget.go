package rework

// Budget tracks bounded automatic-rework progress from a baseline round.
type Budget struct {
	BaselineRound              int
	Attempts                   int
	PreviousFindingFingerprint string
}

// AttemptsAtRound returns the durable attempt count after observing round.
// Persisted progress never moves backward, and rounds before the baseline do
// not produce negative attempts.
func (b Budget) AttemptsAtRound(round int) int {
	attempts := max(round-b.BaselineRound, 0)
	if attempts < b.Attempts {
		return b.Attempts
	}
	return attempts
}

// Recover reconciles a queue snapshot with the latest persisted rework round.
// When baselineRecorded is false, it first reconstructs the baseline used by
// snapshots written before explicit baseline persistence. A newly observed
// fingerprint is retained only when the observed round advances durable
// progress.
func (b Budget) Recover(round int, baselineRecorded bool, fingerprint string) Budget {
	if !baselineRecorded {
		legacy := b
		legacy.PreviousFindingFingerprint = fingerprint
		b.BaselineRound = legacy.LegacySnapshotBaseline(round)
	}
	attempts := b.AttemptsAtRound(round)
	if attempts > b.Attempts {
		b.Attempts = attempts
		if fingerprint != "" {
			b.PreviousFindingFingerprint = fingerprint
		}
	}
	return b
}

// LegacySnapshotBaseline infers the missing baseline in a queue snapshot that
// predates explicit baseline persistence.
func (b Budget) LegacySnapshotBaseline(round int) int {
	baseline := round
	if b.Attempts > 0 {
		baseline -= b.Attempts
	} else if b.PreviousFindingFingerprint != "" {
		// Legacy snapshots could be interrupted after creating the next round but
		// before persisting progress. Preserve that round as the first attempt.
		baseline--
	}
	if baseline < 0 {
		return 0
	}
	return baseline
}

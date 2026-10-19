package testenv

import "testing"

// SkipIfShort keeps a test out of the fast tier. make test runs with -short so
// the inner loop stays fast; make test-full, make verify, and CI run every
// test. Mark tests whose cost (usually real Git processes) dominates suite time.
func SkipIfShort(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("full-tier test; run make test-full")
	}
}

package workspace

import "testing"

func TestSetBaseRefreshStatus(t *testing.T) {
	for _, test := range []struct {
		name        string
		baseSHA     string
		currentSHA  string
		wantBase    string
		wantRefresh string
		wantRebase  string
	}{
		{name: "current", baseSHA: "abc", currentSHA: "abc", wantBase: "current", wantRefresh: "not_needed", wantRebase: "not_needed"},
		{name: "stale", baseSHA: "abc", currentSHA: "def", wantBase: "stale", wantRefresh: "needed", wantRebase: "needed"},
		{name: "unknown", baseSHA: "", currentSHA: "def", wantBase: "unknown", wantRefresh: "unknown", wantRebase: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			metadata := Metadata{BaseSHA: test.baseSHA, BaseCurrentSHA: test.currentSHA}
			setBaseRefreshStatus(&metadata)
			if metadata.BaseStatus != test.wantBase || metadata.RefreshStatus != test.wantRefresh || metadata.RebaseStatus != test.wantRebase {
				t.Fatalf("unexpected status metadata: %#v", metadata)
			}
		})
	}
}

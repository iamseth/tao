package gitops

import "testing"

func TestIsTaoMetadataPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{".tao", true}, {"./.tao/events", true}, {"x/../.tao/a", true}, {".tao/../file", false}, {".tao-other", false}, {"x/.tao", false}, {".git/config", false}, {"", false},
	} {
		if got := IsTaoMetadataPath(tc.path); got != tc.want {
			t.Errorf("%q = %v", tc.path, got)
		}
	}
}

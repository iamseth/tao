package herdr

import "testing"

func TestStripInjectedEnv(t *testing.T) {
	environ := []string{
		"KEEP=value",
		"HERDR_ENV=1",
		"HERDR_SOCKET_PATH=/tmp/herdr.sock",
		"HERDR_PANE_ID=pane-1",
		"HERDR_ENVIRONMENT=unrelated",
		"NO_EQUALS",
		"HERDR_ENV=duplicate",
	}

	filtered := StripInjectedEnv(environ)
	want := []string{"KEEP=value", "HERDR_ENVIRONMENT=unrelated", "NO_EQUALS"}
	if len(filtered) != len(want) {
		t.Fatalf("filtered length = %d, want %d: %#v", len(filtered), len(want), filtered)
	}
	for i := range want {
		if filtered[i] != want[i] {
			t.Fatalf("filtered[%d] = %q, want %q; all values: %#v", i, filtered[i], want[i], filtered)
		}
	}
}

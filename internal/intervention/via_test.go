package intervention

import (
	"strings"
	"testing"
)

func TestParseVia(t *testing.T) {
	t.Parallel()
	exact := strings.Repeat("a", MaxViaBytes)
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "empty", raw: "", want: ""},
		{name: "whitespace only", raw: " \t\n ", want: ""},
		{name: "normal label preserved", raw: "claude-code-workflow wf_43dba409-ce8", want: "claude-code-workflow wf_43dba409-ce8"},
		{name: "surrounding whitespace trimmed", raw: "  relay-bot \t", want: "relay-bot"},
		{name: "exactly max bytes accepted", raw: exact, want: exact},
		{name: "one byte over rejected", raw: exact + "a", wantErr: true},
		{name: "embedded newline rejected", raw: "a\nb", wantErr: true},
		{name: "embedded tab rejected", raw: "a\tb", wantErr: true},
		{name: "embedded nul rejected", raw: "a\x00b", wantErr: true},
		{name: "invalid utf-8 rejected", raw: "a\xffb", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseVia(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseVia(%q) = %q, want error", tt.raw, got)
				}
				if got != "" {
					t.Fatalf("ParseVia(%q) returned %q with error; want empty", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVia(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("ParseVia(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

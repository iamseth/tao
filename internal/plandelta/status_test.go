package plandelta

import (
	"strings"
	"testing"
)

const ordinaryStatus = "1 .M N... 100644 100644 100644 abc def "

func TestParseStatus(t *testing.T) {
	raw := ordinaryStatus + "a\n\t b\x00" + "2 R. N... 100644 100644 100644 abc def R100 new name\x00old\nname\x00" + "u UU N... 100644 100644 100644 100644 abc def aaa conflict\x00? untracked\x00! ignored\x00? .tao/private\x00? .git/private\x00"
	rows, err := parseStatus(raw)
	if err != nil || len(rows) != 5 || rows[0].Path != "a\n\t b" || rows[1].OldPath != "old\nname" || rows[2].Kind != 'u' || rows[3].Kind != '?' || rows[4].Kind != '!' {
		t.Fatalf("%+v %v", rows, err)
	}
	for _, raw := range []string{"? unterminated", "? \x00", "x path\x00", ordinaryStatus + "a", ordinaryStatus + "../escape\x00", strings.Replace(ordinaryStatus, ".M", "XX", 1) + "a\x00", strings.Replace(ordinaryStatus, "100644", "bad", 1) + "a\x00", "2 R. N... 100644 100644 100644 abc def R100 new\x00", ordinaryStatus + "a\x00" + ordinaryStatus + "a\x00"} {
		if rows, err := parseStatus(raw); err == nil {
			t.Fatalf("accepted %q: %+v", raw, rows)
		}
	}
}

func FuzzParseStatus(f *testing.F) {
	for _, seed := range []string{"", "? a\x00", ordinaryStatus + "a\n b\x00", "2 R. N... 100644 100644 100644 abc def R100 new\x00old\x00", "u UU N... 100644 100644 100644 100644 abc def aaa x\x00", "? .tao/noise\x00", "? truncated"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		rows, err := parseStatus(raw)
		if err != nil {
			return
		}
		for _, row := range rows {
			if err := validatePath(row.Path); err != nil {
				t.Fatal(err)
			}
		}
	})
}

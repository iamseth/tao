package testenv

import "testing"

func TestStatusFixture(t *testing.T) {
	for _, tt := range []struct {
		name    string
		records []string
		want    string
	}{
		{name: "empty"},
		{name: "unstaged", records: []string{" M file.go"}, want: " M file.go\x00"},
		{name: "ordered columns", records: []string{"M  staged.go", "?? new.go", " M file.go"}, want: "M  staged.go\x00?? new.go\x00 M file.go\x00"},
		{name: "path spaces", records: []string{" M  file.go "}, want: " M  file.go \x00"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := StatusFixture(tt.records...); got != tt.want {
				t.Fatalf("StatusFixture() = %q, want %q", got, tt.want)
			}
		})
	}
}

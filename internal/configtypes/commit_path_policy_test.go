package configtypes

import (
	"reflect"
	"testing"
)

func TestNormalizeCommitSecretPathPrefixes(t *testing.T) {
	input := []string{`packages\secrets`, "./z//src/", "packages/secrets/", "A"}
	got, err := NormalizeCommitSecretPathPrefixes(input)
	want := []string{"A/", "packages/secrets/", "z/src/"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, %v; want %v", got, err, want)
	}
	got[0] = "changed"
	if input[3] != "A" {
		t.Fatal("input aliased")
	}
	for _, bad := range []string{"", ".", "./", "/", "/etc", `\etc`, `C:secrets`, `./C:secrets`, `C:\secrets`, "x\x00y", "../secrets", "x/../secrets", `x\..\secrets`} {
		t.Run(bad, func(t *testing.T) {
			if _, err := NormalizeCommitSecretPathPrefixes([]string{bad}); err == nil {
				t.Fatalf("accepted %q", bad)
			}
		})
	}
	if got, err := NormalizeCommitSecretPathPrefixes(nil); err != nil || len(got) != 0 {
		t.Fatalf("empty = %v, %v", got, err)
	}
}

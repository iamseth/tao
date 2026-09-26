package randtoken

import (
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	var previous string
	for range 2 {
		token, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if len(token) != 32 {
			t.Fatalf("token length = %d, want 32", len(token))
		}
		if strings.Trim(token, "0123456789abcdef") != "" {
			t.Fatalf("token %q is not lowercase hexadecimal", token)
		}
		if token == previous {
			t.Fatal("successive tokens are identical")
		}
		previous = token
	}
}

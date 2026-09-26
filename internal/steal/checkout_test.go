package steal

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFetchRejectsCompressedTreeBeforeCheckout(t *testing.T) {
	for _, repeated := range []bool{false, true} {
		name, blobBytes := "single", 2<<20
		if repeated {
			name, blobBytes = "repeated", 700<<10
		}
		t.Run(name, func(t *testing.T) {
			fixture := snapshotFixture(t)
			payload := []byte(strings.Repeat("x", blobBytes))
			writeFixtureFile(t, fixture, "large", payload)
			if repeated {
				// Both entries resolve to the same compressed blob. Deduplicating
				// object sizes would incorrectly admit this tree below the cap.
				writeFixtureFile(t, fixture, "duplicate", payload)
			}
			localGit(t, fixture, "add", ".")
			localGit(t, fixture, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "compressed tree")
			unmaterialized := filepath.Join(t.TempDir(), "objects-only")
			localGit(t, fixture, "clone", "--quiet", "--no-checkout", "--", fixture, unmaterialized)
			const capBytes = 1 << 20
			objectBytes, err := ObjectStoreBytes(unmaterialized)
			if err != nil || objectBytes >= capBytes {
				t.Fatalf("compressed metadata = %d, %v; must fit below cap", objectBytes, err)
			}
			source, err := ValidateSourceURL("https://example.com/repo")
			if err != nil {
				t.Fatal(err)
			}
			home, now := t.TempDir(), time.Now()
			scratch := ScratchDir(home, source, now)
			base := fixtureRunner(t, unmaterialized, scratch, "")
			checkout := false
			runner := func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if slices.Contains(args, "checkout") {
					checkout = true
					return errors.New("checkout must not run")
				}
				return base(ctx, cwd, name, args, stdout, stderr)
			}
			_, err = Fetch(context.Background(), Options{DataHome: home, Source: source, Now: func() time.Time { return now }, Runner: runner, MaxBytes: capBytes})
			if checkout || err == nil || !strings.Contains(err.Error(), "expanded checkout tree exceeds") {
				t.Fatalf("checkout = %v, error = %v", checkout, err)
			}
			if _, err := os.Lstat(scratch); !os.IsNotExist(err) {
				t.Fatalf("rejected scratch remains: %v", err)
			}
		})
	}
}

func TestCheckCheckoutSize(t *testing.T) {
	for _, tt := range []struct {
		name, output string
		wantErr      string
	}{
		{"empty", "", ""},
		{"exact", "blob 6\x00blob 4\x00", ""},
		{"repeated", "blob 6\x00blob 6\x00", "exceeds"},
		{"submodule", "commit -\x00blob 10\x00", ""},
		{"negative", "blob -1\x00", "invalid"},
		{"missing", "blob -\x00", "invalid"},
		{"overflow", "blob 9223372036854775808\x00", "invalid"},
		{"tree", "tree 1\x00", "invalid"},
		{"empty record", "\x00", "invalid"},
		{"truncated", "blob 1", "incomplete"},
		{"oversized record", strings.Repeat("x", 129), "oversized"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := func(_ context.Context, _, _ string, _ []string, stdout, _ io.Writer) error {
				// Arbitrary runner write boundaries must not change accounting.
				for _, b := range []byte(tt.output) {
					if _, err := stdout.Write([]byte{b}); err != nil {
						return err
					}
				}
				return nil
			}
			err := checkCheckoutSize(context.Background(), "unused", runner, 10)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v; want %q", err, tt.wantErr)
			}
		})
	}
}

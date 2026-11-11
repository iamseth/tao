package commandrunner

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"
)

func TestGateEnvTransport(t *testing.T) {
	t.Setenv(SliceCompletionOwnerEnv, "private")
	t.Setenv("CI", "inherited")
	for i := range 4 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			value := fmt.Sprintf("literal-$%d", i)
			pins := map[string]string{"CI": value}
			ctx := WithGateEnv(context.Background(), pins)
			pins["CI"] = "mutated"
			var out bytes.Buffer
			err := DefaultLocal(ctx, t.TempDir(), "sh", []string{"-c", `printf '%s|%s' "$CI" "$TAO_SLICE_COMPLETION_OWNER"`}, &out, &out)
			if err != nil || out.String() != value+"|" {
				t.Fatal(out.String(), err)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if err := DefaultLocal(canceled, "", "sh", []string{"-c", "exit 0"}, nil, nil); err == nil {
				t.Fatal("ignored cancellation")
			}
		})
	}
}

func TestGateEnvRunningCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(WithGateEnv(context.Background(), map[string]string{"CI": "pinned"}), 30*time.Millisecond)
	defer cancel()
	if err := DefaultLocal(ctx, t.TempDir(), "sh", []string{"-c", "exec sleep 30"}, nil, nil); err == nil {
		t.Fatal("canceled gate succeeded")
	}
}

func TestGateEnvRejectsInvalidPins(t *testing.T) {
	if err := DefaultLocal(WithGateEnv(context.Background(), map[string]string{"PATH": "bad"}), "", "sh", []string{"-c", "exit 0"}, nil, nil); err == nil {
		t.Fatal("accepted invalid pins")
	}
}

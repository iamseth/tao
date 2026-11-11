package process

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"
)

func TestGateEnvTransport(t *testing.T) {
	t.Setenv("HERDR_ENV", "injected")
	t.Setenv("TAO_SLICE_COMPLETION_OWNER", "old")
	for i := range 4 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			value := fmt.Sprintf("literal-$%d", i)
			pins := map[string]string{"CI": value}
			ctx := WithGateEnv(context.Background(), pins)
			pins["CI"] = "mutated"
			ended := make(chan struct{})
			ctx = WithSessionLifetime(ctx, []string{"TAO_SLICE_COMPLETION_OWNER=private"}, func() { close(ended) })
			p, err := DefaultProcessStarter(ctx, t.TempDir(), "sh", []string{"-c", `printf '%s|%s|%s' "$CI" "$HERDR_ENV" "$TAO_SLICE_COMPLETION_OWNER"`})
			if err != nil {
				t.Fatal(err)
			}
			out, err := io.ReadAll(p.Stdout())
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Wait(); err != nil {
				t.Fatal(err)
			}
			<-ended
			if string(out) != value+"||private" {
				t.Fatal(string(out))
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := DefaultProcessStarter(canceled, "", "sh", nil); err == nil {
				t.Fatal("ignored cancellation")
			}
		})
	}
}

func TestGateEnvRunningCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(WithGateEnv(context.Background(), map[string]string{"CI": "pinned"}))
	defer cancel()
	ended := make(chan struct{})
	ctx = WithSessionLifetime(ctx, nil, func() { close(ended) })
	p, err := DefaultProcessStarter(ctx, t.TempDir(), "sh", []string{"-c", "exec sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		_ = p.Kill()
		t.Fatal("cancellation did not end provider")
	}
	if err := p.Wait(); err == nil {
		t.Fatal("canceled provider succeeded")
	}
}

func TestGateEnvRejectsInvalidPins(t *testing.T) {
	if _, err := DefaultProcessStarter(WithGateEnv(context.Background(), map[string]string{"PATH": "bad"}), "", "sh", nil); err == nil {
		t.Fatal("accepted invalid pins")
	}
}

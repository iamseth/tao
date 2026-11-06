package run

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

type ownedPlanResolver func(context.Context, string) (*plan.PlanDetail, error)

func (r ownedPlanResolver) ResolvePlan(ctx context.Context, input string) (*plan.PlanDetail, error) {
	return r(ctx, input)
}

func ownedPlanScope(ctx context.Context, _ *plan.PlanDetail, operation func(context.Context) error) error {
	return operation(ctx)
}

func TestOwnedPlanOrdering(t *testing.T) {
	var steps []string
	initial := &plan.PlanDetail{Dir: "exact-directory"}
	refreshed := &plan.PlanDetail{Dir: initial.Dir}
	stamp := time.Now()
	resolver := ownedPlanResolver(func(_ context.Context, input string) (*plan.PlanDetail, error) {
		steps = append(steps, input)
		if input == "selector" {
			return initial, nil
		}
		if input != "exact-directory" {
			t.Fatalf("reload selector: %q", input)
		}
		return refreshed, nil
	})
	options := OwnedPlanOptions{StartedAt: stamp, Scope: func(ctx context.Context, detail *plan.PlanDetail, op func(context.Context) error) error {
		if detail != initial {
			t.Fatal("scope did not receive initial detail")
		}
		steps = append(steps, "scope")
		err := op(ctx)
		steps = append(steps, "scope-release")
		return err
	}}
	wantErr := errors.New("callback")
	err := withOwnedPlan(context.Background(), resolver, "selector", options, func(_ context.Context, detail *plan.PlanDetail) error {
		if detail != refreshed {
			t.Fatal("operation used stale detail")
		}
		steps = append(steps, "operation")
		return wantErr
	}, func(ctx context.Context, detail *plan.PlanDetail, started time.Time, op func(context.Context) error) error {
		if detail != initial || started != stamp {
			t.Fatal("lock inputs changed")
		}
		steps = append(steps, "lock")
		err := op(ctx)
		steps = append(steps, "unlock")
		return err
	})
	if err != wantErr { //nolint:errorlint // The boundary must preserve exact callback error identity.
		t.Fatalf("error identity: %v", err)
	}
	if want := []string{"selector", "scope", "lock", "exact-directory", "operation", "unlock", "scope-release"}; !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps: %v", steps)
	}
}

func TestOwnedPlanRefusals(t *testing.T) {
	for _, scenario := range []string{"nil-context", "nil-resolver", "nil-scope", "nil-callback", "empty-selector", "canceled-scope", "initial-missing", "reload-missing", "initial-error", "reload-error", "abandoned", "scope-error", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			detail := &plan.PlanDetail{Dir: t.TempDir(), State: plan.State{Plan: plan.PlanState{ID: "test"}}}
			calls := 0
			wantErr := errors.New("failure")
			var resolver plan.Resolver = ownedPlanResolver(func(context.Context, string) (*plan.PlanDetail, error) {
				calls++
				if scenario == "initial-missing" || scenario == "reload-missing" && calls == 2 {
					return nil, nil
				}
				if scenario == "initial-error" || scenario == "reload-error" && calls == 2 {
					return nil, wantErr
				}
				if scenario == "abandoned" && calls == 2 {
					copy := *detail
					copy.State.Status = plan.StatusAbandoned
					return &copy, nil
				}
				return detail, nil
			})
			ctx := context.Background()
			options := OwnedPlanOptions{Scope: ownedPlanScope}
			operation := func(context.Context, *plan.PlanDetail) error { t.Fatal("operation reached"); return nil }
			switch scenario {
			case "nil-context":
				ctx = nil
			case "nil-resolver":
				resolver = nil
			case "nil-scope":
				options.Scope = nil
			case "nil-callback":
				operation = nil
			case "scope-error":
				options.Scope = func(context.Context, *plan.PlanDetail, func(context.Context) error) error { return wantErr }
			case "canceled-scope":
				options.Scope = func(ctx context.Context, _ *plan.PlanDetail, op func(context.Context) error) error {
					canceled, cancel := context.WithCancel(ctx)
					cancel()
					return op(canceled)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			input := "test"
			if scenario == "empty-selector" {
				input = " "
			}
			err := WithOwnedPlan(ctx, resolver, input, options, operation)
			if err == nil {
				t.Fatal("expected refusal")
			}
			if strings.HasSuffix(scenario, "error") && err != wantErr { //nolint:errorlint // Scope and resolver failures must not be wrapped or reclassified.
				t.Fatalf("error identity: %v", err)
			}
			if errors.Is(err, errOwnedPlanAbandoned) != (scenario == "abandoned") {
				t.Fatalf("abandonment classification: %v", err)
			}
			if scenario == "abandoned" && err.Error() != "plan test is abandoned" {
				t.Fatalf("message: %v", err)
			}
			if strings.HasPrefix(scenario, "canceled") && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation: %v", err)
			}
			// Failures must not leave plan ownership behind.
			if err := WithPlanRunLock(context.Background(), detail, time.Now(), func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOwnedPlanRealLockLifetime(t *testing.T) {
	detail := &plan.PlanDetail{Dir: t.TempDir(), State: plan.State{Plan: plan.PlanState{ID: "test"}}}
	resolver := ownedPlanResolver(func(context.Context, string) (*plan.PlanDetail, error) { return detail, nil })
	options := OwnedPlanOptions{Scope: ownedPlanScope}
	wantErr := errors.New("operation failed")
	err := WithOwnedPlan(context.Background(), resolver, "test", options, func(ctx context.Context, _ *plan.PlanDetail) error {
		if err := WithOwnedPlan(ctx, resolver, "test", options, func(context.Context, *plan.PlanDetail) error { return nil }); err != nil {
			t.Fatalf("nested: %v", err)
		}
		err := WithOwnedPlan(context.Background(), resolver, "test", options, func(context.Context, *plan.PlanDetail) error { t.Fatal("contender entered"); return nil })
		if !errors.Is(err, plan.ErrRunLocked) || !errors.Is(err, ErrCannotStart) {
			t.Fatalf("contention: %v", err)
		}
		return wantErr
	})
	if err != wantErr { //nolint:errorlint // Nested ownership must preserve exact callback error identity.
		t.Fatalf("callback: %v", err)
	}
	if err := WithOwnedPlan(context.Background(), resolver, "test", options, func(context.Context, *plan.PlanDetail) error { return nil }); err != nil {
		t.Fatalf("release: %v", err)
	}
}

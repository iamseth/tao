// Package run invokes configured agents against Tao plan slices.
//
// It owns Tao plan orchestration: resolving runnable work, rendering prompts,
// writing plan logs and metrics, and verifying that successful agent invocations
// actually advanced the plan. Execution-boundary recovery is coordinated by
// ExecutionBoundaryController; its policy kernel is ClassifyInterruptedSlice,
// and physical root identity is delegated to internal/workspace. Durable
// recovery effects stop at the PlanMutationRecord boundary backed by
// internal/plan.PlanRecord. Run authorizes workspace boundary repair from live
// Git and completion evidence, then requests a typed compare-and-set stamp; it
// does not edit plan state or choose persistence primitives. Runtime request
// defaulting lives in
// internal/runtimeconfig. Run may append explicitly best-effort outcome events,
// but it does not select plan artifact persistence primitives for lifecycle
// transitions.
//
// Agent dispatch is registry-driven: instead of switching on agent kind, run
// resolves an agent.Descriptor via agent.Lookup and delegates each bounded
// provider call to internal/agentsession. Run retains plan log framing, event
// shaping, and slice-only budget enforcement; per-kind policy lives on the
// descriptor. Low-level agent protocol and process mechanics live under
// internal/agent.
package run

# Tao Configuration

## How settings resolve

Run settings resolve in three stages:

1. Environment values and built-in defaults establish the baseline.
2. Repository defaults from `tao repo config` override that baseline.
3. Explicit per-run flags win over both, including explicit `false` and `0` values.

Tao does not load `.env` files. Repository defaults cover pull requests,
model selection, maximum rework attempts, and the rework escalation threshold;
`unset` removes only the named stored default and restores inheritance.
For example:

```sh
tao repo config --pull-request true
tao repo config --pull-request false
tao repo config --pull-request unset
tao repo config --max-rework-attempts 0 # explicit zero disables automatic rework
tao repo config --max-rework-attempts unset # restore environment/default inheritance
tao repo config --rework-escalation-from-attempt 4
tao repo config --rework-escalation-from-attempt unset
```

## Runtime settings

Defaults below describe the environment/built-in layer, before repository and
per-run overrides. Boolean values use the grammar described below. Model names
are opaque runtime-specific identifiers: non-empty and whitespace-free after
trimming surrounding whitespace. Leave a model variable unset to inherit;
explicitly setting it to an empty value is invalid.

| Setting | Default | Accepted values | Purpose |
| --- | --- | --- | --- |
| `TAO_COMMIT_POLICY` | `slice` | `slice`, `none` | Automatic per-slice commits or manual commits. Historical `plan` metadata remains readable, but new runs reject it. |
| `TAO_EXECUTION_MODE` | `isolated` | `isolated`, `current` | Use a feature branch/worktree or the launch checkout and branch. |
| `TAO_AGENT` | `pi` | `pi`, `claude` | Select the agent runtime. |
| `TAO_SESSION_TIMEOUT` | `20m` | Non-negative Go duration; `0` disables | Wall-clock limit for run-path agent sessions, not interactive planning. |
| `TAO_SESSION_WARN_PERCENT` | `80` | Integer 0–99; `0` disables | Requests one advisory wrap-up notice for implementation/rework sessions. Unsupported runtimes skip delivery; Pi delivers between turns without extending the deadline. |
| `TAO_MODEL` | Unset (runtime selection) | Model name | Shared base for unset roles, planning generation, PR work, and standalone merge-message generation. |
| `TAO_RUN_MODEL` | Unset (resolved base) | Model name | Implementation and rework slices. |
| `TAO_REVIEW_MODEL` | Unset (resolved base) | Model name | Plan review and proposal correction. |
| `TAO_MERGE_REVIEW_MODEL` | Unset (resolved base) | Model name | Aggregate merge review. |
| `TAO_RESOLVER_MODEL` | Unset (resolved base) | Model name | Merge conflict and rework resolution. |
| `TAO_REWORK_ESCALATION_MODEL` | Unset (disabled) | Model name | Opt-in model for late automatic-rework rounds. |
| `TAO_UPDATE` | `warn` | `warn`, `auto`, `off` | Report updates, permit automatic installation, or disable automatic update checks. |
| `TAO_PULL_REQUEST` | `false` | Boolean | Enable pull-request finalization after successful execution and approval. |
| `TAO_REVIEW` | `true` | Boolean | Enable the post-execution plan review. |
| `TAO_AUTO_REWORK` | unset | Deprecated boolean alias | Accepted for one release: false maps to zero attempts, true to five. `TAO_MAX_REWORK_ATTEMPTS` wins when both are supplied, even if the ignored alias is invalid. Supplied aliases warn; unset aliases are hidden in status/settings. |
| `TAO_MAX_REWORK_ATTEMPTS` | `5` | Non-negative integer | Bound automatic-rework cycles; `0` disables them. Does not affect merge-batch review. |
| `TAO_MERGE_REVIEW_MAX_ATTEMPTS` | `5` | Non-negative integer | Bound aggregate merge-review attempts independently of run rework settings; `0` allows no attempts. A positive explicit merge-review option overrides this setting; a zero option uses it. |
| `TAO_REWORK_ESCALATION_FROM_ATTEMPT` | `4` | Integer at least `1` | First escalation-eligible attempt in each automatic-rework window; a threshold above the attempt count is valid. |
| `TAO_DANGEROUSLY_SKIP_PERMISSIONS` | `false` | Boolean | Skip Claude permission checks; compatibility no-op for Pi. |
| `TAO_MERGE_VERIFY_COMMAND` | Auto-detect | Command string, including empty | Override merge verification; explicitly empty disables verification. |
| `TAO_AGGREGATE_REVIEW_CONVERGENCE_WINDOW` | `2` | Integer at least `2` | Consecutive changes-requested rounds considered for aggregate-review non-convergence. |
| `TAO_APPROVED_BY` | Unset | Approver name | Default identity for `tao approve` when `--by` is absent. |
| `TAO_RUN_HEADER` | `true` | Boolean | Show the best-effort TTY-only run header. |
| `TAO_PLANNER_ROUTING` | Unset (`off`) | `off`, `shadow`, `randomized` | Planner routing mode for `tao note run` only. |
| `TAO_PLANNER_ROUTING_ARMS` | Unset (`pi,claude`, equal weights) | Unique `pi`/`claude` comma list, either all bare or all weighted (e.g. `pi=0.7,claude=0.3`); finite non-negative weights summing to `1` | Configure eligible routing arms and probabilities. |
| `TAO_PLANNER_ROUTING_FLOOR` | Unset (`0.1`) | Finite number from `0` to `0.5` | Minimum configured arm probability in randomized mode. |
| `TAO_THEME` | `tokyonight` | `tokyonight`, `gruvbox`, or alias `default` (trimmed, case-insensitive) | Shared CLI/TUI palette. |

## Budgets

Agent budgets use one `TAO_BUDGET_<SCOPE>_<METRIC>_WARN` scheme, where `<SCOPE>`
is `SLICE` or `PLAN` and `<METRIC>` is `OUTPUT_TOKENS`, `COST`, `TOOL_CALLS`,
`ASSISTANT_MESSAGES`, or `ERRORED_MESSAGES`. Crossing a `WARN` value only records
an advisory warning. The two `STOP` caps are enforced: crossing one records
`budget_exceeded` and blocks the slice. Each `STOP` value must be at least its
`WARN` value. The built-in defaults are:

```sh
TAO_BUDGET_SLICE_OUTPUT_TOKENS_WARN=40000
TAO_BUDGET_SLICE_COST_WARN=5
TAO_BUDGET_SLICE_TOOL_CALLS_WARN=120
TAO_BUDGET_SLICE_ASSISTANT_MESSAGES_WARN=80
TAO_BUDGET_SLICE_ERRORED_MESSAGES_WARN=0   # warn on any
TAO_BUDGET_PLAN_OUTPUT_TOKENS_WARN=150000
TAO_BUDGET_PLAN_COST_WARN=20
TAO_BUDGET_PLAN_TOOL_CALLS_WARN=400
TAO_BUDGET_PLAN_ASSISTANT_MESSAGES_WARN=300
TAO_BUDGET_PLAN_ERRORED_MESSAGES_WARN=0    # warn on any
TAO_BUDGET_SLICE_OUTPUT_TOKENS_STOP=       # disabled by default; explicit 0 is a hard cap
TAO_BUDGET_SLICE_COST_STOP=                # disabled by default; explicit 0 is a hard cap
```

The former `TAO_BUDGET_<SCOPE>_<METRIC>` and `TAO_MAX_SLICE_*` names are
accepted as deprecated aliases for one release, yield to a set canonical key,
and appear in `tao status` only when set.

The canonical warning settings are:

| Metric | Slice setting | Plan setting |
| --- | --- | --- |
| Output tokens | `TAO_BUDGET_SLICE_OUTPUT_TOKENS_WARN` | `TAO_BUDGET_PLAN_OUTPUT_TOKENS_WARN` |
| Cost | `TAO_BUDGET_SLICE_COST_WARN` | `TAO_BUDGET_PLAN_COST_WARN` |
| Tool calls | `TAO_BUDGET_SLICE_TOOL_CALLS_WARN` | `TAO_BUDGET_PLAN_TOOL_CALLS_WARN` |
| Assistant messages | `TAO_BUDGET_SLICE_ASSISTANT_MESSAGES_WARN` | `TAO_BUDGET_PLAN_ASSISTANT_MESSAGES_WARN` |
| Errored messages | `TAO_BUDGET_SLICE_ERRORED_MESSAGES_WARN` | `TAO_BUDGET_PLAN_ERRORED_MESSAGES_WARN` |

Only `TAO_BUDGET_SLICE_OUTPUT_TOKENS_STOP` and `TAO_BUDGET_SLICE_COST_STOP`
are supported hard caps. Counts accept non-negative integers; costs accept
finite non-negative numbers. Unset or empty stop values leave the cap disabled;
an explicit `0` is a hard cap and requires the corresponding warning to be `0`.

Automatic rework defaults to five attempts with escalation eligible from attempt four.
Repository flags `--max-rework-attempts N|unset` (non-negative) and
`--rework-escalation-from-attempt N|unset` (at least one) validate before writing;
unmentioned settings are preserved. `tao repo config` displays absent values as
`unset` and explicit zero as `0`; status and Settings show numeric repository
values with source `repository` while retaining captured environment diagnostics.

Automatic rework resolves environment → repository numeric defaults → explicit invocation flags. `--max-rework-attempts N` and `--rework-escalation-from-attempt N` preserve explicit zero/count overrides; only the attempt count permits zero. The deprecated `--auto-rework` alias remains accepted for one release (false = zero, true = five), warns whenever supplied, and yields to an explicit count in the same invocation. Registered flag defaults are not overrides. Positive attempts with automatic review disabled normalize to zero with one warning, regardless of source. Reverify always executes and presents zero attempts. Note promotion keeps automatic rework disabled. Invalid consumed environment settings are rejected lazily before execution/handoff mutation; unrelated merge settings do not block ordinary runs.

## Model selection

Model settings are optional; unset roles inherit the resolved base model, and
with no model settings Tao leaves the runtime's selection unchanged. Environment
values establish the base and role fields, repository defaults override matching
fields, and an explicit per-invocation `--model` overrides the base and all roles.
Manage the current repository's defaults independently:

```sh
tao repo config --model provider/model
tao repo config --run-model provider/implementation-model
tao repo config --run-model unset # remove this default and restore inheritance
tao repo config --rework-escalation-model provider/stronger-model
```

Repository model flags are `--model`, `--run-model`, `--review-model`,
`--merge-review-model`, `--resolver-model`, and `--rework-escalation-model`;
each accepts a name or `unset`.
For a one-invocation override of every role, use `tao run --model NAME <plan>`,
`tao review --run --model NAME <plan>`, or `tao merge --model NAME <plan>`
(including `tao merge --all --model NAME`). See the
[model selection guide](usage-guide.md#choose-models-for-agent-sessions)
for precedence and runtime rejection behavior.

Escalation is opt-in and separate from role selection: use
`tao run --rework-escalation-model NAME <plan>` for a one-run policy override.
The attempt threshold is environment-only (default 4); without an effective
escalation model, rework is unchanged. A model already recorded for a round
still wins at execution time. See the
[rework guide](usage-guide.md#escalate-late-automatic-rework)
for attempt counting and durable selection.

## Planner routing

Planner routing applies only to `tao note run`:

- `TAO_PLANNER_ROUTING` = `off|shadow|randomized`: `off` is the default; shadow records assignments without changing the planner, randomized selects its runtime.
- `TAO_PLANNER_ROUTING_ARMS` = `pi,claude`: default equal-weight runtimes; alternatively use explicit probabilities summing to 1, such as `pi=0.7,claude=0.3`.
- `TAO_PLANNER_ROUTING_FLOOR` = `0.1`: default minimum configured arm probability in randomized mode; accepts a finite value from 0 to 0.5.

`tao note run --planner-routing MODE` overrides the mode for one invocation;
`--planner-arm pi|claude` records a manual override to an installed eligible arm
when routing is enabled (shadow still leaves the planner unchanged). Provider,
model, and reasoning effort remain inherited. See the
[usage guide](usage-guide.md#planner-routing-for-tao-note-run) before enabling
randomized routing.

## Boolean grammar and invalid values

Runtime-table settings are captured once per invocation. Boolean settings accept
trimmed, case-insensitive `true/false`, `1/0`, `yes/no`, `on/off`, `t/f`, and `y/n`.
For example, `TAO_RUN_HEADER=false` disables the TTY-only run header.

Invalid settings are rejected by name only when an operation consumes them,
including invalid advisory-budget overrides and hard caps; unused settings do
not block unrelated commands. Invalid `TAO_THEME` and `TAO_RUN_HEADER` values
instead warn and retain their defaults (`tokyonight` and enabled).
Help and `tao status` (`--json` for automation) remain available to diagnose
invalid configuration, including `TAO_UPDATE`. Settings/Debug remain explicit
developer previews (`tui-preview --view settings` or `--view debug`), not
interactive dashboard tabs. Use environment settings and `tao repo config`
to configure runtime and repository defaults.
Diagnostics retain every runtime-table setting, even when another is invalid.

Run `tao status` to see the resolved `TAO_*` runtime values and repository plan
rollups (`tao status --json` for automation). Use `tao run --help` and
`tao merge --help` for exact one-run overrides covering review, rework, pull
requests, permissions, and integration. Configure the agent session timeout with
`TAO_SESSION_TIMEOUT`; set it to `0` to disable the timeout.

## Other environment variables

These variables are read outside the runtime settings table:

- `TAO_DATA_HOME`: override Tao's data directory. Resolution uses this value
  first, then `XDG_DATA_HOME` with `/tao` appended, then `HOME` with
  `/.local/share/tao` appended (normally `~/.local/share/tao`). If all three are
  unset or empty, the fallback is `./.local/share/tao`.
- `TAO_PI_EXTENSION_DIR`: prompt-install override for the Pi extension **source**
  directory (containing `package.json`), not its installation destination. By
  default Tao discovers `extensions/pi` from the working directory, executable,
  or build-source location.
- `TAO_CLAUDE_COMMANDS_DIR`: prompt-install override for the Claude commands
  directory; defaults to `~/.claude/commands`.
- `TAO_SLICE_COMPLETION_OWNER`: internal handshake binding slice completion to
  its active driver and lifetime. Operators must not set it; it is not a user
  setting or recovery authority.

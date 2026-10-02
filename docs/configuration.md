# Tao Configuration

## How settings resolve

Settings resolve in this order, with the rightmost explicit value winning:
**built-in → global → repository → environment → flags**.
Absent fields inherit; explicit `false` and `0` do not. Tao does not load `.env`
files. Environment values are captured once per invocation. Direct runs select
settings from the plan-recorded repository, not the launch checkout; note runs
use the selected registered repository. Unhealthy repositories remain
inspectable but are not runnable.

### Read and write durable settings

```sh
tao config                         # same as get, current registered repository
tao config get pull_request
tao config set pull_request true
tao config set models.model provider/model --global
tao config set session_timeout 30m --repo tao
tao config unset pull_request       # remove only this repository's value
```

`tao config get [key]`, `set <key> <value>`, and `unset <key>` accept
`--global` or `--repo ID`, never both. With neither, the current checkout must
be registered (`tao init`). `--repo` accepts a unique ID prefix or exact name;
`--global` works outside a repository. Omitting the verb lists settings.
`get` reports the effective value/source and the selected scope's stored value;
writing a saved value does not defeat an environment override.

For one release, `tao repo config [<repo-id>]` remains a compatibility adapter
for repository `--pull-request true|false|unset` and model flags `--model`,
`--run-model`, `--review-model`, `--merge-review-model`, `--resolver-model`,
and `--rework-escalation-model` (each accepts a name or `unset`). Prefer
`tao config` for new scripts. No automatic migration is required.

### Storage and canonical keys

Global settings live in `<data-home>/config.json` (normally
`~/.local/share/tao/config.json`), with schema `tao.config.v1`:

```json
{
  "schema": "tao.config.v1",
  "settings": {
    "agent": "pi",
    "pull_request": false,
    "session_timeout": "30m",
    "models": {"model": "provider/model"},
    "budget": {"slice": {"cost": {"warn": 5, "stop": null}}}
  }
}
```

Repository settings stay in `<data-home>/repos/<repo-id>/repo.json`, schema
`tao.repo.v1`, under the existing `run_defaults` object; its nested `models`
and `pull_request` representation remains readable. Missing global files or
missing repository defaults mean inheritance. These files are local-only:
do not add them to a checkout. CLI and Settings writes share serialized atomic
updates that preserve unrelated metadata and saved fields.

CLI keys are dotted paths; JSON uses nested objects, not literal dotted keys.
Values are native JSON booleans/numbers/strings, not stringified booleans or
numbers. Durations, enums, and model names are strings. CLI values use the
runtime parsers below, then save native scalars.

| Scope | Canonical keys | Native value |
| --- | --- | --- |
| Global and repository | `agent`, `review_agent`, `commit_policy`, `execution_mode`, `session_timeout` | String; choices/ranges below |
| Global and repository | `pull_request`, `review_enabled`, `dangerously_skip_permissions`, `run_header` | Boolean |
| Global and repository | `max_slices`, `session_warn_percent`, `max_rework_attempts`, `rework_escalation_from_attempt` | Integer |
| Global and repository | `models.model`, `models.run_model`, `models.review_model`, `models.merge_review_model`, `models.resolver_model`, `models.rework_escalation_model` | Non-empty model string |
| Global and repository | `budget.<slice\|plan>.<metric>.warn` | Non-negative count integer or cost number |
| Global and repository | `budget.slice.output_tokens.stop`, `budget.slice.cost.stop` | Non-negative integer/number or `null` |
| Global only | `theme`, `update` | String; choices below |
| Environment/flags only (not persisted) | `merge_verify_command`, `aggregate_review_convergence_window`, `approved_by`, `planner_routing`, `planner_routing_arms`, `planner_routing_floor` | Existing process/merge/planner controls; see below and command help |

Budget metrics are `output_tokens`, `cost`, `tool_calls`, `assistant_messages`,
and `errored_messages`. `max_slices` has no environment alias: it defaults to
`0` (all pending slices) and accepts non-negative integers. Other defaults and
environment aliases appear below (`review_enabled` maps to `TAO_REVIEW`). The
deprecated `TAO_AUTO_REWORK` alias is environment-only; save `max_rework_attempts`
instead.
Bootstrap paths and recovery actions are never persisted settings.

### Migration: environment now wins

Previously a repository value could mask a `TAO_*` value. That order is
intentionally reversed. For example, after saving `pull_request=true`:

```sh
tao config set pull_request true
TAO_PULL_REQUEST=false tao run <plan>  # now false, formerly repository true won
TAO_PULL_REQUEST=false tao run --pull-request=true <plan> # explicit flag wins
```

Similarly, `TAO_MODEL=provider/env-model` now wins over a repository
`models.model=provider/repo-model`. Remove old exports from your shell/CI to
use saved preferences; `tao config unset` removes only the selected saved
field, not an environment value. Inspect `tao status` before migrating a run.

### Invocation flags

`tao run` and `tao note run` add only `--agent pi|claude` and
`--session-timeout DURATION` (non-negative Go duration; `0` disables).
All existing flags retain their names and behavior, including `--max-slices`,
`--commit-policy`, `--execution-mode`, `--pull-request`, `--no-review`, and
`--dangerously-skip-permissions`. The permissions flag bypasses Claude
permission checks and is a compatibility no-op for Pi, exactly like
`TAO_DANGEROUSLY_SKIP_PERMISSIONS`.

Run-specific flags such as `--model`, `--rework-escalation-model`,
`--no-run-header`, `--review-agent`, `--max-rework-attempts`,
`--rework-escalation-from-attempt`, and recovery flags are unchanged; note-run planner-routing flags are unchanged. This does not add
all run flags to note run or change merge policy. Use `tao run --help`,
`tao note run --help`, and `tao merge --help` for each command's full flag set.

## Command applicability

Defaults are command-specific, not live settings for every command. The matrix
below covers shared option composition for six paths; `yes` means consumed, not
that the command registers a corresponding flag. `note run` and `rework --run`
use the full-run profile. `review --run` runs a fresh review, not implementation
slices. `merge` covers both single and batch agent sessions and does not create
PRs or inherit automatic slice-commit behavior.

| Setting | run | note run | rework --run | review --run | merge | prompt run |
| --- | --- | --- | --- | --- | --- | --- |
| `--max-slices` | yes | yes | yes | — | — | — |
| `--continue` | yes | yes | yes | — | — | — |
| `TAO_COMMIT_POLICY` | yes | yes | yes | — | — | yes |
| `TAO_EXECUTION_MODE` | yes | yes | yes | — | — | yes |
| `TAO_AGENT` | yes | yes | yes | yes | yes | — |
| `TAO_REVIEW_AGENT` | yes | yes | yes | yes | — | — |
| `TAO_PULL_REQUEST` | yes | yes | yes | — | — | — |
| `TAO_REVIEW` | yes | yes | yes | — | — | — |
| `TAO_SESSION_TIMEOUT` | yes | yes | yes | yes | yes | — |
| `TAO_MODEL` | yes | yes | yes | yes | yes | — |
| `TAO_RUN_MODEL` | yes | yes | yes | — | — | — |
| `TAO_REVIEW_MODEL` | yes | yes | yes | yes | — | — |
| `TAO_MERGE_REVIEW_MODEL` | — | — | — | — | yes | — |
| `TAO_RESOLVER_MODEL` | — | — | — | — | yes | — |
| `TAO_REWORK_ESCALATION_MODEL` | yes | yes | yes | — | — | — |
| `TAO_AUTO_REWORK` | yes | yes | yes | — | — | — |
| `TAO_MAX_REWORK_ATTEMPTS` | yes | yes | yes | — | — | — |
| `TAO_RUN_HEADER` | yes | yes | yes | — | — | — |
| `TAO_REWORK_ESCALATION_FROM_ATTEMPT` | yes | yes | yes | — | — | — |
| `TAO_DANGEROUSLY_SKIP_PERMISSIONS` | yes | yes | yes | yes | yes | — |

This is not a table of all runtime consumers. Budgets, planning routing, theme,
merge verification and other settings retain their independent consumers and
validation. Planning agent/model selection remains separate from execution.
Applicability conveys no lifecycle, recovery, approval, commit or merge authority;
for example, the full-run profile does not grant note execution recovery flags.

Precedence follows the scoped order above: built-ins → global settings → repository
settings → captured invocation environment → explicitly provided registered flags.
Only persisted settings participate below the environment; environment-only
controls still come from the captured environment. An explicit flag is an override
even when its value equals the built-in default, including explicit `false`; an
absent or unregistered flag is never an override. Unset model roles fall back to the
resolved base (escalation remains opt-in). The invocation reuses its composed
snapshot through execution handoffs.

Only consumed settings are admitted. A malformed applicable environment value
still fails even if a repository default or flag would override it; unrelated
invalid settings do not block the path. Applicable conflicts are strict, with
winning sources (`default`, `global`, `repository`, `env`, `flag`) in option-conflict
diagnostics: PR with commit policy `none`, or PR in the current workspace, is an
error, not a silent PR fallback. Positive automatic-rework attempts with
review disabled normalize to zero with a warning, including explicit flags. State-dependent checks still run on the execution path.

`note run` inherits automatic-rework policy, attempt limits, escalation and the
TTY-only header preference, as well as models, permissions and session timeout.
Execution options are validated before durable note promotion. Both ordinary
and PR-thread `rework --run` resolve and validate the full-run handoff before
persisting rework; they carry the resolved options into execution. Rework without
`--run` does not admit execution-only settings.

`prompt run` consumes only commit policy and execution mode. Rendering does not
look up repository defaults or require repository registration; other prompts
consume neither setting. Existing prompt flags remain accepted for compatibility,
including `--commit=false` to suppress commit instructions. Rendering starts no
agent session and does not validate unrelated execution settings.

Human-readable `tao status` annotates shared settings with applicable commands;
its values and source overlay are still defaults, not an execution admission
result. Status JSON and TUI Settings retain their existing shapes and values.

## Runtime settings

Defaults below are built-in values, before saved settings, environment, and
explicit flags. Boolean values use the grammar described below. Model names
are opaque runtime-specific identifiers: non-empty and whitespace-free after
trimming surrounding whitespace. Leave a model variable unset to inherit;
explicitly setting it to an empty value is invalid.

| Setting | Default | Accepted values | Purpose |
| --- | --- | --- | --- |
| `TAO_COMMIT_POLICY` | `slice` | `slice`, `none` | Automatic per-slice commits or manual commits. Historical `plan` metadata remains readable, but new runs reject it. |
| `TAO_EXECUTION_MODE` | `isolated` | `isolated`, `current` | Use a feature branch/worktree or the launch checkout and branch. |
| `TAO_AGENT` | `pi` | `pi`, `claude` | Select the agent runtime. |
| `TAO_REVIEW_AGENT` | empty (inherit) | empty, `pi`, `claude` | Select the plan review runtime independently. Empty/unset inherits the final implementing runtime; repository and invocation selectors override the environment. Model selection remains independent. |
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
finite non-negative numbers. An empty environment STOP value disables the cap;
an explicit `0` is a hard cap and requires the corresponding warning to be `0`.
Absent environment keys inherit saved caps (disabled by default). In saved settings, `null` explicitly disables a STOP cap while
`tao config unset` restores inheritance. Null is not accepted for other settings.
STOP/WARN relationships are checked on the fully resolved values when consumed,
not on an incomplete saved layer; saving a scalar does not prove admission.

```sh
tao config set budget.slice.cost.stop 10 --global
tao config set budget.slice.cost.stop null # disable inherited cap in this repo
tao config unset budget.slice.cost.stop    # inherit global cap again
```

Automatic rework defaults to five attempts with escalation eligible from attempt four.
`max_rework_attempts` (non-negative) and `rework_escalation_from_attempt` (at
least one) are scoped settings; `tao config set` and the compatibility flags
`tao repo config --max-rework-attempts N|unset` and
`--rework-escalation-from-attempt N|unset` validate before writing, and
unmentioned settings are preserved. `tao repo config` displays absent values as
`unset` and explicit zero as `0`; status and Settings show saved numeric values
with their winning source while retaining captured environment diagnostics.

Automatic rework resolves global → repository numeric defaults → environment → explicit invocation flags. `--max-rework-attempts N` and `--rework-escalation-from-attempt N` preserve explicit zero/count overrides; only the attempt count permits zero. The deprecated `--auto-rework` alias remains accepted for one release (false = zero, true = five), warns whenever supplied, and yields to an explicit count in the same invocation. Registered flag defaults are not overrides. Positive attempts with automatic review disabled normalize to zero with one warning, regardless of source. Reverify always executes and presents zero attempts. Note promotion inherits the selected repository's automatic-rework settings. Invalid consumed environment settings are rejected lazily before execution/handoff mutation; unrelated merge settings do not block ordinary runs.

## Review runtime selection

`review_agent` is a scoped setting (global and repository) with environment
alias `TAO_REVIEW_AGENT`, so plan reviews resolve it like every other run
preference: built-in → global → repository `review_agent` → `TAO_REVIEW_AGENT`
→ explicit `--review-agent`. Only `pi` and `claude` are selectors. An unset
selector inherits the final implementing `TAO_AGENT`; omitted invocation flags
preserve saved values. Legacy and unregistered repositories inherit normally. An
explicitly empty CLI selector is invalid.

```sh
tao config set review_agent claude          # current registered repository
tao config set review_agent claude --global
tao run --review-agent pi my-plan
tao review --run --review-agent claude my-plan
tao rework --from-pr --run --review-agent pi my-plan
tao config unset review_agent               # remove only this saved value
tao repo config --review-agent unset        # compatibility adapter, same effect
```

`review` and `rework` require `--run` with this flag. Explicit selection survives
rework handoffs and automatic rounds; PR triage keeps its existing runtime.
`note run` inherits review configuration but has no new selector flag.
`--no-review` does not require the unused reviewer runtime to be available.
Invalid selectors are rejected before execution or reopening work.

Runtime and model selection are independent: `TAO_REVIEW_MODEL` and repository
`--review-model` still select the review model, with existing base-model fallback
and invocation `--model` precedence. Tao passes model names unchanged; runtime or
model rejection never triggers fallback to another runtime or model. Plan-review
failures retain their existing best-effort handling. This selector does not affect
implementation, PR creation, or any merge-owned session. `tao merge` has no
`--review-agent` flag.

`tao doctor` (or `--verbose`) shows implementation and effective plan-review
roles from the composed settings (saved global and repository values beneath the
captured environment), not a future invocation's flags. Outside a registered checkout it uses environment inheritance;
if repository lookup is unavailable, it says so. Only role settings are consumed:
unrelated malformed model, timeout, or budget settings do not prevent collection.
Missing selected executables get setup guidance; an unused second runtime is not
required. Pi's bounded passive readiness probe runs once when either role selects
Pi; Claude retains executable and prompt checks. These checks do not authenticate
remotely or prove provider availability or compatibility with the review model.
`install-prompts` and `install-prompts --check` cover every installed supported
runtime regardless of the selected roles, and install nothing if none is found.

## Model selection

Model settings are optional; unset roles inherit the resolved base model, and
with no model settings Tao leaves the runtime's selection unchanged. Each field
uses the scoped precedence above; role fallback happens after resolution. A
saved base never erases an explicit environment role. An explicit per-invocation
`--model` overrides the base and all roles. Manage defaults independently:

```sh
tao config set models.model provider/model --global
tao config set models.run_model provider/implementation-model
tao config unset models.run_model # restore inheritance
tao config set models.rework_escalation_model provider/stronger-model
```
For a one-invocation override of every role, use `tao run --model NAME <plan>`,
`tao review --run --model NAME <plan>`, or `tao merge --model NAME <plan>`
(including `tao merge --all --model NAME`). See the
[model selection guide](usage-guide.md#choose-models-for-agent-sessions)
for precedence and runtime rejection behavior.

Escalation is opt-in and separate from role selection: use
`tao run --rework-escalation-model NAME <plan>` for a one-run policy override.
The attempt threshold is `rework_escalation_from_attempt` (default 4), also
settable via `TAO_REWORK_ESCALATION_FROM_ATTEMPT`; without an effective
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
invalid configuration, including `TAO_UPDATE`. Diagnostics retain every
runtime-table setting even when another is invalid. Invalid saved fields remain
diagnostic/admission errors even when a valid environment value masks them;
fix or unset the saved field rather than treating the override as repair.
Unknown keys, disallowed scopes, invalid scalars, and unsupported schemas report
errors. Malformed files (including duplicate or ambiguous keys) require manual
repair at the reported path; Tao does not silently replace them. Readable files
can have individual invalid/unknown saved fields repaired or unset without
losing unrelated fields. A write followed by diagnostic output may have saved
successfully while another field remains invalid; inspect `get` before retrying.

`tao status` shows effective scoped settings, their sources, retained global and
repository values, environment masking, invalid diagnostics, and repository plan
rollups. Its JSON retains `runtime_env` alongside scoped settings and read-only
path facts. Paths are reported separately, never as editable preferences.
`tao config get` distinguishes selected-scope storage from effective values;
`source=env` with a saved value explains why changing that saved value has no
immediate effect. Inspection does not supply invocation flags for a future run.

The `tao ui` Settings tab shows global-effective and repository-effective
settings. Repository values are editable with typed validation, inherit/unset,
STOP disabling, and confirmation; global-only and process/path rows are
read-only. Use `tao config --global` to edit global preferences. File values
refresh through the shared service, while the UI's captured environment stays
fixed until a new invocation. See the [workflow guide](usage-guide.md#choose-durable-settings-or-one-off-overrides)
for editing keys. Debug and developer previews remain available for diagnostics.

## Other environment variables

These variables are read outside the runtime settings table. Bootstrap paths
are environment/flag-only (`--plans-dir` still selects runtime plan storage);
they cannot be saved with `tao config`. Status/Settings display captured resolved
paths without installing anything. No new Pi prompt override was introduced.

- `TAO_DATA_HOME`: override Tao's data directory. Resolution uses this value
  first, then `XDG_DATA_HOME` with `/tao` appended, then `HOME` with
  `/.local/share/tao` appended (normally `~/.local/share/tao`). If all three are
  unset or empty, the fallback is `./.local/share/tao`.
- `TAO_PI_EXTENSION_DIR`: prompt-install override for the Pi extension **source**
  directory (containing `package.json`), not its installation destination. By
  default Tao discovers `extensions/pi` from the working directory, executable,
  or build-source location. The Pi extension installation destination defaults
  to `~/.pi/agent/extensions/tao`; it is distinct from that source directory.
- `PI_CODING_AGENT_DIR`: existing Pi agent-directory override. Pi prompts and
  extensions install under its `prompts/` and `extensions/tao` children;
  by default the agent directory is `~/.pi/agent`. This is not a new
  Tao-specific prompt override.
- `TAO_CLAUDE_COMMANDS_DIR`: prompt-install override for the Claude commands
  directory; defaults to `~/.claude/commands`.
- `TAO_SLICE_COMPLETION_OWNER`: internal handshake binding slice completion to
  its active driver and lifetime. Operators must not set it; it is not a user
  setting or recovery authority.

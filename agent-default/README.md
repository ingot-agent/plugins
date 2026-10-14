# agent-default

`agent.default` supplies the session-aware model/tool loop, validated history,
streaming execution, observation delivery, and persistent child-session scheduling.
See the [plugin documentation index](../docs/README.md).

## Identity, components, and dependencies

The directory/module suffix is `agent-default`; [the manifest](ingot.plugin.toml)
names `agent.default` and supports Ingot `>=0.3.0 <0.4.0`. It declares three
components, each constructed with `New(ctx, deps)`:

| Component / package | Dependencies | Exports |
| --- | --- | --- |
| `observation` / `./observation` | `[]observation.Observer` | `observation.Consumer` |
| `session-tree` / `./sessiontree` | `state.Scope`; optional `agent.ChildSessionRepository` and `workspace.Manager` | `agent.Children`, `prompt.Contributor`, internal `sessioncontrol.Control`, `[]operation.Operation` |
| `default` / `.` | See below | `agent.Runtime`, `agent.Control`, `agent.History`, `[]operation.Operation` |

The default component consumes `state.Scope`, `model.Runtime`, `tool.Runtime`,
`session.Store`, `session.Manager`, `asset.Store`, `prompt.Renderer`, `observation.Consumer`, and
`sessioncontrol.Control`. It also consumes ordered `[]agent.Interceptor` and
`[]agent.RoundInterceptor`, plus explicit ABI optional
`model.StreamingRuntime`, `model.RequestResolver`, `contextwindow.Compactor`, and
`agent.PluginInputProjector` capabilities. The composite
plugin supplies its own observation/control components. Direct Go construction
can omit observation (discarding events) and control (disabling dispatch), but
the required model/tool/store/asset/prompt/state dependencies cannot be nil.

## Main loop configuration

`config.toml` is owned by this plugin within its assigned runtime state scope.
It is not a `[plugins.agent.default]` build-recipe configuration table.

```toml
max_rounds = 8
# Optional generation overrides; omit to inherit provider behavior:
# temperature = 0.2
# max_tokens = 4096
```

| Field | Default | Meaning |
| --- | --- | --- |
| `temperature` | absent | Optional finite number in `[0,2]`; explicit zero is an override |
| `max_tokens` | absent | Optional positive output-token limit |
| `max_rounds` | 8 | Maximum model rounds per turn; zero selects 8, negatives fail |
| `streaming` | ignored | Deprecated compatibility field; does not enable or disable streaming |

Missing configuration applies defaults. Malformed TOML and unknown fields fail
startup. A turn snapshots these settings; later updates affect subsequent turns.

Operation `config`, group `agent-default` (`/agent-default config`), accepts `{}`
with no extra input properties. It offers the round limit and explicit
`inherit`/`override` modes for temperature and token limit. Override values are
collected in a second typed interaction. Saving validates generation limits,
detects conflicting persisted edits, writes the file, publishes defaults, and
returns `{"restart_required":false}`. Direct file edits are loaded at construction.

Model selection belongs to [model-runtime](../model-runtime/README.md). Use the
WebUI model picker or `/model-runtime config` to select the default provider,
model, and reasoning effort. The retired `provider`, `model`, `reasoning_effort`,
and `provider_default_reasoning` keys are accepted in old Agent configuration
files but ignored, and are dropped on the next save. They are not copied into
another plugin's state. If an old Agent override was your only model selection,
select it once in model-runtime; existing runtime defaults take effect immediately.

When `model.RequestResolver` is available, the Agent captures the runtime's
selection once at turn entry and supplies it to every model round, including
streaming and compaction. A later picker update affects subsequent turns.
Provider-default reasoning is pinned with the request-only `providerDefault`
sentinel. The Agent does not persist this snapshot or enumerate providers.
Without a resolver, model selection is left to the model runtime on each call.

## Turn, history, and streaming behavior

The session must already exist and be active. SendMessage accepts one Input and
returns after admission. The Agent owns the per-session FIFO, pause state and one
active executor. Session Manager checks happen before admission without taking
the Turn gate; public submission to a child is rejected. Different sessions can
run concurrently. Browser/request cancellation does not cancel accepted work.

The executor loads and recovers history, prepares and atomically appends the
input's optional file notice plus actual user message, and renders the prompt.
After each complete Round, it freezes the pending snapshot and prepares every
input in order. The successful prefix is appended to Turn.Inputs and rendered
once for the next model call. The final actual user message is Prompt.Input;
all preceding messages are Prompt.History. Later arrivals wait until the next
boundary. A file preparation failure fails that input, pauses the suffix and
lets the current Turn continue. Storage, prompt or model failures terminate the
Turn. Pause survives completion and an empty queue until explicit resume.

Tool calls in a decision still execute sequentially. Approval, ask and subagent
waits must finish before ordinary input can enter. MaxRounds, configuration,
hooks, accounting root and cancellation are shared by the whole lifecycle.


The assistant decision is persisted before its tools execute; each tool result is
persisted after execution. Unknown tools and invalid arguments become diagnostic
tool messages so the model can correct its request. Other tool execution errors
stop the turn. A tool side effect followed by persistence failure is not rolled
back and must not be blindly retried.

New history uses `agent.message` v2, retaining opaque provider replay state and
tool argument bytes exactly, including whitespace and JSON escape spelling.
Version 1 records remain readable and are not rewritten; older Agent binaries
cannot read new v2 records. Replay state survives tool rounds, reloads and snapshot
cloning independently of visible content. Replay support uses SDK v0.2.18;
validate this branch with the matching local SDK workspace; release checks require published SendMessage contracts.

The final allowed root round still advertises ordinary tools, but a returned tool
call fails with `ErrMaxRounds` before that decision is committed. Child sessions retain their
result-submission boundary as described below. A normal assistant decision with
no tool calls ends the turn only after checking the boundary queue. Pending input
without remaining budget is left for the next lifecycle.

Turn interceptors wrap the turn; round interceptors see immutable invocation and
response facts plus a policy-editable decision. The implementation validates
decision mutations, permits only a valid text/content-only short-circuit result,
and rejects repeated terminal execution or rewriting a result after it has been
committed. See [round.go](round.go) for the precise extension contract.

Provider retries remain owned by ModelRuntime. The Agent applies StreamRetry to
only the current round's failed attempt, flushes pending progress before the
replacement snapshot, and retains earlier rounds and completed tools. Retry
state is included in ExecutionBlock.Retry and cleared on resumed output or
StreamRecovered. Retractions use validated UTF-8 byte boundaries. This does not
reintroduce the public Run/Stream APIs or per-delta snapshots.

Control.State returns the full ordered input/round state and final Execution,
including status, duration, failures and canonical Result on success. Watch
returns revision-ordered RuntimeUpdates: affected lifecycle/receipt/queue state
at structural boundaries, or coalesced text/tool increments. Progress flushes
every 50 ms or 4 KiB and before structural changes; it does not clone retained
turns. A bounded 128-update window lets independent watchers catch up; a watcher
outside that window receives a full Reset snapshot. State includes pending
progress in its revision, so delayed increments cannot duplicate recovered text.
Snapshot capture copies mutable slice elements under the queue mutex, then
deep-copies immutable payloads and sorts outside the mutex.
The last 128 completed lifecycles and their input receipts are retained together;
unattached failed/canceled receipts are also bounded. Pending and active items
are never evicted. RequestID deduplication lasts for retained records. Queue and
result state do not survive restart. Stop cancels the full Turn and pauses the
pending suffix; cleanup cancels and joins both root workers and child dispatch.

Provider token settlement remains at Session level. RootSessionID defaults to
SessionID for ordinary submissions; followups inherit their source accounting
root. An active lifecycle cannot change accounting owner.

Model streaming is used when injected, otherwise Complete is used. Pre-output
model.ErrStreamingUnsupported may fall back to Complete; partial output is never
retried. Reasoning is transient. Watch/observation delivery failures do not cancel
execution. The deprecated streaming configuration field remains ignored.


History uses version-1 `agent.message` entries for individual messages. Unknown versions, malformed
payloads, duplicate call identities, and invalid tool-result ordering fail rather
than being silently repaired. `History.Load` is read-only. At the next execution,
unanswered calls in a trailing interrupted round receive explicit diagnostic
results recording an unknown outcome; their tools are not executed again.
Inline non-text content is materialized into `asset.Store` before persistence.

## Plugin Context Inputs (Unreleased)

Plugins inject `agent.PluginInputWriter` and append `agent.PluginInput` to an
explicitly selected Session. [context-input](../context-input/README.md) owns
validation, versioned records, escaped user-message formatting and Store writes.
The Agent optionally injects `agent.PluginInputProjector`, delegating recognition
and projection of non-agent records to it. Recognized invalid records fail with
their Entry position; without a projector, non-agent entries remain opaque and
are skipped. Neither wire formats nor envelopes are built into the Agent.

This branch requires the matching local SDK and atomic batch Store. Use a
separate workspace containing SDK and the plugins for development; published
module pins must be updated together before release. Existing databases and
agent.message version 1 need no migration.


An input persisted between a tool decision and its results is buffered only
during history projection and emitted after every matching result. If the
trailing round is incomplete, read-only History defers the input. The next Turn
adds interrupted results and reloads durable history, including deferred
inputs. Crashes discard the temporary slice, not committed records. This does
not retry original tools or provide exactly-once delivery/processing.

Plugin inputs committed before a history load are included at Turn start or
when admitting a new batch of user inputs. A plugin append alone does not trigger
a per-Round history reload or start execution. Inputs written after the snapshot
may wait for a later load, preserving the existing visibility contract.

Web file uploads now travel with their actual user Input. Agent-generated file
notices and actual user messages are standard agent.message records committed in
one Store.Append transaction. Independent PluginInputWriter calls continue to
write their own records, before or after that atomic pair. Old plugin-input file
notices remain readable, without rewriting old history.


## Child-agent configuration

The separate `subagents.toml` in the same plugin state scope is loaded at startup
by the session-tree component. Its independent Operation is `/agent-default subagents`
(name `subagents`, group `agent-default`), accepting `{}` with no extra input
properties. `/agent-default config` continues to edit only the main loop settings.
Changes saved through this Operation take effect immediately. Direct file edits
are loaded only when the runtime is reconstructed.

The Operation first asks for an explicit mode:

- **Built-in types** removes the override file and restores automatic defaults.
- **Custom types** opens a structured replacement list covering names,
  descriptions, system prompts, tool allowlists, allowed child types, and root
  allowed types. A missing file seeds the form with the installed built-ins.
  Removing an entry deletes that type; update references when renaming or
  removing types. An empty list of definitions disables child types.
- **Disabled** writes an explicit empty configuration, suppressing built-ins.

Tool choices come from the composed runtime's validated tool definitions. Type
references accept names declared in the same form, including newly added types;
existing names are suggestions. Saving applies startup validation, including
tool availability and required child-storage/workspace capabilities, rejects
concurrent persisted edits, and atomically replaces the file (or removes it for
built-in mode). Cancellation or unavailable interaction leaves state unchanged.
The configuration version is managed by the Operation.

After persistence succeeds, the Operation atomically publishes an immutable
configuration snapshot and returns `{"restart_required":false}`. Type discovery
and subsequent child creations use the new snapshot, including updated root
permissions. A creation already in progress retains its captured snapshot.
Existing queued/running children keep their frozen prompts, tools, and child
permissions. A removed type is no longer discoverable or available for new
dispatch, including from an existing child's frozen permission list.

Disabling child types prevents new creations while accepted tasks continue.
Existing children retain prompt contribution, query, wait, cancellation, result
submission, and interruption on parent cancellation or runtime shutdown.
Re-enabling types takes effect immediately. Startup recovery runs once whenever
child storage is available, even if child creation is disabled; live configuration
updates never run recovery or interrupt accepted tasks. Validation, cancellation,
conflict, or persistence failures leave the active snapshot unchanged.

Tool discovery must finish before the Operation can run; it does not add a
`tool.Runtime` dependency to the session-tree component.

**When the file is absent**
and the composed tool runtime provides `submit_agent_result` (from `tool-subagent`),
three built-in root types are enabled if the composition also provides child
Session storage and a workspace manager (for example `session-sqlite`):

| Type | Purpose | Allowed tools when installed |
| --- | --- | --- |
| `coder` | Bounded implementation tasks | `read_file`, `search`, `edit_file`, `shell_exec`, `submit_agent_result` |
| `explorer` | Read-only investigation | `read_file`, `search`, `submit_agent_result` |
| `reviewer` | Read-only code review | `read_file`, `search`, `submit_agent_result` |

Unavailable optional tools are omitted from each built-in allowlist; all types
always have `submit_agent_result`, and none may spawn further children. Install
`tool-edit` for reading/searching/editing and `tool-shell` for shell commands;
with only `tool-subagent`, the built-ins have no inspection or editing tools.
Shell commands run with host privileges, so `coder` is **not** a security sandbox;
use `interceptor-approval` for interactive approval if needed. Without
`submit_agent_result`, the missing file leaves child support disabled, preserving
compositions that do not include `tool-subagent`. A missing storage or workspace
capability with `tool-subagent` installed is a startup error, not silent fallback.

A **present** file replaces the built-ins completely (even if it contains an
empty `agents` list). Use this to disable or customize the default types:

```toml
subagents_config_version = 1
root_allowed_types = ["researcher"]

[[agents]]
name = "researcher"
description = "Investigates a bounded question and reports findings."
system_prompt = "Complete the assigned research task and submit a concise result."
tools = ["submit_agent_result"]
allowed_child_types = []
```

This minimal example requires [tool-subagent](../tool-subagent/README.md), which
provides `submit_agent_result`; add other installed tool names when the task needs
them. Every allowed tool must exist in the composed tool runtime.

| Field | Contract |
| --- | --- |
| `subagents_config_version` | Required and exactly `1` for a present file |
| `root_allowed_types` | Unique declared types available to a root session; empty permits none |
| `agents[].name` | Unique identifier matching `[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*` |
| `agents[].description` | Nonempty UTF-8 description for discovery |
| `agents[].system_prompt` | Nonempty UTF-8 prompt contributed to that child's session |
| `agents[].tools` | Unique valid tool identifiers; must include `submit_agent_result` |
| `agents[].allowed_child_types` | Unique declared types that this type may spawn; empty permits none |

Unknown fields, duplicate identifiers, missing referenced types, or missing tool
definitions fail validation. Enabling any type requires both a child-session
repository and workspace manager, supplied by
[session-sqlite](../session-sqlite/README.md). Each accepted child stores a frozen
definition and digest, task/context, parent/root/depth, and lifecycle metadata.
It receives an explicit workspace binding; the scheduler does not create an
isolated checkout automatically.

Children execute one accepted task. Only the frozen tool allowlist is available.
`submit_agent_result` must be the only tool call in its round and is unavailable to
roots. A child's ordinary prose response is not a submitted result. The final
round allows only submission. Cancellation and completion use the persistent
child lifecycle instead of permitting arbitrary new turns on a child session.

Current limits are constants, not TOML settings: maximum depth 8, queued children
64, active children (including queued work) 128, combined task/context 256 KiB,
result 256 KiB, task deadline 30 minutes, and independent settlement timeout
10 seconds. On restart, queued/working children are marked interrupted rather
than resumed. For previously running work, `execution_stopped` can be unknown;
that state is not evidence that external processes have exited.

## Observation and implementation checks

The observation component delivers cloned events asynchronously to observers in
composition order. With no observers it provides a discard consumer. It assigns
per-turn sequences and bounds pending progress events at 1024; progress may be
dropped under pressure. Lifecycle events and canonical execution results remain
separate from optional progress delivery.

See [runtime.go](runtime.go), [execute.go](execute.go), [round.go](round.go),
[history.go](history.go), [stream.go](stream.go),
[sessiontree/config.go](sessiontree/config.go), and
[observation/hub.go](observation/hub.go). Run `go test ./...` in this module.
Existing round, execution outcome, stream, recovery, setup, and child-tree
tests cover these contracts, including [round_contract_test.go](round_contract_test.go),
[subagent_runtime_test.go](subagent_runtime_test.go),
[sessiontree/setup_test.go](sessiontree/setup_test.go), and
[sessiontree/tree_test.go](sessiontree/tree_test.go).
See [CONTRIBUTING](../CONTRIBUTING.md) for workspace setup.

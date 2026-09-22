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
| `session-tree` / `./sessiontree` | `state.Scope`; optional `agent.ChildSessionRepository` and `workspace.Manager` | `agent.Children`, `prompt.Contributor`, internal `sessioncontrol.Control` |
| `default` / `.` | See below | `agent.Runtime`, `agent.StreamingRuntime`, `agent.History`, `[]operation.Operation` |

The default component consumes `state.Scope`, `model.Runtime`, `tool.Runtime`,
`session.Store`, `asset.Store`, `prompt.Renderer`, `observation.Consumer`, and
`sessioncontrol.Control`. It also consumes ordered `[]model.ProviderSource`,
`[]agent.Interceptor`, and `[]agent.RoundInterceptor`, plus explicit ABI optional
`model.StreamingRuntime` and `contextwindow.Compactor` capabilities. The composite
plugin supplies its own observation/control components. Direct Go construction
can omit observation (discarding events) and control (disabling dispatch), but
the required model/tool/store/asset/prompt/state dependencies cannot be nil.

## Main loop configuration

`config.toml` is owned by this plugin within its assigned runtime state scope.
It is not a `[plugins.agent.default]` build-recipe configuration table.

```toml
provider = ""
model = ""
max_rounds = 8
# Optional generation overrides; omit to inherit provider behavior:
# temperature = 0.2
# max_tokens = 4096
```

| Field | Default | Meaning |
| --- | --- | --- |
| `provider` | empty | Uses `model.runtime` selection when empty |
| `model` | empty | Uses `model.runtime` default model when empty |
| `temperature` | absent | Optional finite number in `[0,2]`; explicit zero is an override |
| `max_tokens` | absent | Optional positive output-token limit |
| `max_rounds` | 8 | Maximum model rounds per turn; zero selects 8, negatives fail |
| `streaming` | ignored | Deprecated compatibility field; does not enable or disable streaming |

Missing configuration applies defaults. Malformed TOML and unknown fields fail
startup. A turn snapshots these settings; later updates affect subsequent turns.

Operation `config`, group `agent-default` (`/agent-default config`), accepts `{}`
with no extra input properties. It offers current providers, a model name, round
limit, and explicit `inherit`/`override` modes for temperature and token limit.
Override values are collected in a second typed interaction. Saving validates
the current provider choice, detects conflicting persisted edits, writes the
file, publishes defaults, and returns `{"restart_required":false}`. Direct file
edits are loaded at construction.

## Turn, history, and streaming behavior

The session must already exist. `Run` and `Stream` serialize work per session,
load and validate history, recover any trailing incomplete tool round, persist
the current user message, render the prompt, and run model/tool rounds. Calls in
one model decision execute sequentially through `tool.Runtime`, carrying the
turn's session ID as their explicit execution scope. Different sessions can run
concurrently.

The assistant decision is persisted before its tools execute; each tool result is
persisted after execution. Unknown tools and invalid arguments become diagnostic
tool messages so the model can correct its request. Other tool execution errors
stop the turn. A tool side effect followed by persistence failure is not rolled
back and must not be blindly retried.

The final allowed root round still advertises ordinary tools, but a returned tool
call fails with `ErrMaxRounds` before that decision is committed. Child sessions retain their
result-submission boundary as described below. A normal assistant decision with
no tool calls ends the turn.

Turn interceptors wrap the turn; round interceptors see immutable invocation and
response facts plus a policy-editable decision. The implementation validates
decision mutations, permits only a valid text/content-only short-circuit result,
and rejects repeated terminal execution or rewriting a result after it has been
committed. See [round.go](round.go) for the precise extension contract.

`Run` and `Stream` return `agent.Execution`, including execution outcome/accounting
and a canonical result on success. Accounting distinguishes complete, partial,
and unavailable provider-reported token usage; a missing usage report is not
treated as an observed zero-token call.

`Stream` requires a handler. With no model streaming capability it uses complete
invocation. It also falls back to complete when streaming returns
`model.ErrStreamingUnsupported` before any mapped stream event has been delivered.
It does not retry after partial output or a consumer error. Streaming reasoning
is transient; it is not persisted as canonical assistant content. The deprecated
`streaming` TOML field is ignored; the caller selects the API.

History uses version-1 `agent.message` session entries. Unknown versions, malformed
payloads, duplicate call identities, and invalid tool-result ordering fail rather
than being silently repaired. `History.Load` is read-only. At the next execution,
unanswered calls in a trailing interrupted round receive explicit diagnostic
results recording an unknown outcome; their tools are not executed again.
Inline non-text content is materialized into `asset.Store` before persistence.

## Child-agent configuration

The separate `subagents.toml` in the same plugin state scope is read once by the
session-tree component. It is not edited by `/agent-default config`, and changing
it requires restarting/reconstructing the runtime. Missing file or an empty
`agents` list disables child-agent support; child-management calls then return
`agent.ErrChildUnsupported`.

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
Existing round, execution/accounting, stream, recovery, setup, and child-tree
tests cover these contracts, including [round_contract_test.go](round_contract_test.go),
[subagent_runtime_test.go](subagent_runtime_test.go), and
[sessiontree/tree_test.go](sessiontree/tree_test.go).
See [CONTRIBUTING](../CONTRIBUTING.md) for workspace setup.

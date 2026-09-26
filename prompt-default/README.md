# prompt-default

`prompt.default` builds model messages from configured system text, ordered prompt
contributors, persisted conversation history, and current user input. See the
[plugin documentation index](../docs/README.md).

## Identity and capabilities

The directory is `prompt-default`; [the manifest](ingot.plugin.toml) names the
plugin `prompt.default`, with component `default` in `.` and Ingot compatibility
`>=0.3.0 <0.4.0`. [New](promptdefault.go) takes `ctx` and dependencies containing
`State state.Scope` and `Contributors []prompt.Contributor`. The contributor
collection may be empty; nil elements are rejected. Exports are
`Renderer prompt.Renderer` and `Operations []operation.Operation`.

## Configuration and operation

The plugin owns `config.toml` inside its runtime-assigned state scope. These are
runtime settings, not build-recipe `plugins.toml` fields:

```toml
system_prompt = "You are a helpful assistant."
max_block_bytes = 65536
max_system_bytes = 262144
```

| Field | Default | Meaning |
| --- | --- | --- |
| `system_prompt` | empty string | Static text preceding contributor blocks |
| `max_block_bytes` | 64 KiB | Maximum content size of one contributed block |
| `max_system_bytes` | 256 KiB | Maximum combined system content including headings and separators |

Zero numeric limits select defaults; negatives are invalid. The configured text
must be UTF-8 and fit within `max_system_bytes`. Missing configuration is valid
and starts with empty system text. Malformed TOML and unknown fields fail startup.

The `config` operation in group `prompt-default` (`/prompt-default config`) accepts
`{}` with no extra input fields and collects these three settings interactively.
It validates, atomically saves, and publishes a new renderer configuration, then
returns `{"restart_required":false}`. Existing renders retain their configuration
snapshot; later renders use the new one. Persisted changes made during interaction
return `ErrConfigConflict`. Direct edits to the file are read on construction.

## Rendering contract

`Render` invokes contributors sequentially in the injected collection's order.
Each receives its own cloned request, so one contributor cannot alter another's
input. The resulting message sequence is:

1. One system message when configured text or contributed blocks exist.
2. The supplied history, in its existing order.
3. One user message containing the current input, including valid attachments.

Each block is prefixed with `## <block name>\n`; adjacent blocks are separated by
two newlines. Configured system text is also separated from the first block by
two newlines. Block names must be nonempty UTF-8 and contain no CR/LF. Blocks
remain in contributor order; names are not used to sort or deduplicate them.

Byte limits count UTF-8 text bytes and inline media bytes. URI/asset references do
not cause referenced files to be opened or their sizes to be charged by this
renderer. `max_block_bytes` excludes the heading, while the total system limit
includes headings and separators. An oversized or invalid block returns
`ErrInvalidBlock`; an oversized combined system prompt returns `ErrSystemLimit`.
Contributors can return typed media content, but the selected model adapter may
reject media in system messages.

This renderer validates content and composes messages; it does not implement
template substitution, load workspace files itself, truncate history, count
tokens, or enforce model-specific role ordering. Such behavior belongs to
contributors, the agent, the compactor, or the provider.

## Source and checks

See [promptdefault.go](promptdefault.go), [config.go](config.go), and
[setup.go](setup.go). Run `go test ./...` in this module. Existing tests in
[promptdefault_test.go](promptdefault_test.go), [live_test.go](live_test.go), and
[m1_state_test.go](m1_state_test.go) cover composition, limits, ownership, and live
configuration. Workspace instructions are in [CONTRIBUTING](../CONTRIBUTING.md).

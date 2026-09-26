# Live official plugin configuration

Date: 2026-09-22

Fifteen official plugins apply successful configuration Operation updates to
the running process and return `restart_required:false`. The catalog contains
19 modules and 16 configuration Operations: `app-webui` is the exception whose
changed HTTP listener, buffer and server settings require a restart.
`session-sqlite`, `skill-runtime` and `tool-subagent` expose no configuration
Operation. See the complete [activation matrix](configuration.md#activation-boundaries).

The guarantees below describe the 15 live Operations. They do not apply to
WebUI's restart-based settings or `agent-default`'s separately loaded
`subagents.toml` definitions. Read each module's README for that boundary.

The model-provider-specific directory and request-capture behavior remains
documented in
[live model provider configuration](live-model-provider-configuration.md).

## Commit and publication boundary

Each live configuration Operation keeps the existing optimistic transaction:

1. load the persisted source configuration;
2. collect and validate interaction answers;
3. prepare the complete immutable runtime candidate;
4. recheck cancellation and reject stale persisted state under the plugin-local
   commit lock;
5. persist the configuration atomically; and
6. publish the already-prepared runtime state through a no-fail in-process
   update before returning success.

Validation, cancellation, conflict, or persistence failure leaves the active
runtime state unchanged. Once persistence succeeds, publication cannot be
canceled or reported as a failed save.

## Invocation snapshots

An invocation captures one coherent configuration. Configuration publication
does not rewrite a request that is already running. Calls that begin after the
configuration Operation completes observe the new state.

- Agent turns retain their agent-level provider/model overrides, generation
  settings and round limit across rounds. Empty provider/model overrides are
  resolved by `model-runtime` on each model call; runtime defaults and provider
  connection snapshots can therefore change between rounds of one Turn.
- Context compaction, prompt rendering, approval, usage counting, and tool
  invocation retain one snapshot for the call.
- HTTP requests finish on the client they started with; the update swaps in a
  prepared transport for later requests and closes the old idle pool.
- Script interception exports one stable dispatcher for each target. A call
  captures the current ordered hook list, so hooks can be added, removed,
  reordered, or moved between targets without rebuilding the component graph.
- Asset writes complete under their captured storage limits. Capacity changes
  wait for active writes, reject a new total limit below durable usage, and
  update a cancellation-aware concurrency limiter for later I/O.
- Usage route publication clears the bounded cache and installs the new route
  table and capacity together.

## Boundaries

This behavior applies to configuration saved through the plugin's Operation on
an already-loaded plugin instance. Editing plugin state files directly does not
trigger reload. Installing or replacing plugin binaries still follows the
runtime image build and startup workflow. Dependency collections that are not
configuration, such as the set of tools or prompt contributors assembled by
the component graph, remain fixed unless the plugin explicitly exposes a live
typed source for them.

## Verification

The live plugins have regression coverage that invokes their configuration Operation
and verifies the existing exported capability observes the new state. Dynamic
hook dispatch, HTTP transport replacement, asset capacity and concurrency
state, route/cache publication, stale-write rejection, and failed persistence
remain covered by module tests and race-enabled CI.

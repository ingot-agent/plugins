# Live model provider configuration

This document describes the model-specific part of the repository-wide
[live official plugin configuration](live-plugin-configuration.md) contract.

The configuration Operations of `model-openai-compatible`,
`model-openai-responses`, and `model-runtime` apply successful changes to the
running process. Adding a provider to an already-loaded plugin makes it
available to later configuration forms and model calls. Changing the global
default provider and model takes effect for subsequent calls with omitted
selection fields. Successful results preserve their existing output shapes
and return `restart_required:false`.

## Provider source contract

Provider plugins export `Source model.ProviderSource`. Consumers inject
`ProviderSources []model.ProviderSource`. The SDK defines these types without
an ABI dependency:

```go
type ProviderEntry struct {
    Name     string
    Models   []ModelEntry
    Complete func(context.Context, Request) (Response, error)
    Stream   StreamNext
}

type ProviderSource interface {
    Snapshot(context.Context) ([]ProviderEntry, error)
}
```

A source is always present, including before initial configuration, when its
snapshot is empty. It returns a caller-owned slice of current entries.
`Complete` is required; `Stream` is optional. The runtime invokes these
functions directly and returns `ErrStreamingUnsupported` when `Stream` is nil.
Each model entry names an available model and the explicit reasoning-effort
values it supports. An empty model directory preserves compatibility with
providers that do not expose closed model discovery.

Functions retain immutable configuration, support concurrent calls, and remain
usable after the source publishes another snapshot.

Consumers read sources in dependency order and reject empty names, duplicate
names, and nil `Complete` functions. A duplicate across sources
rejects the combined directory rather than selecting an arbitrary provider.
Sources may return errors; consumers preserve those errors. Runtime startup
validates its source dependencies but does not require a valid initial
directory, so provider configuration remains available to repair conflicts.

## Activation and request lifetime

Provider configuration validates and constructs an immutable candidate before
committing. The existing optimistic conflict check and atomic file replacement
remain in effect. Publication happens only after persistence succeeds;
validation, cancellation before commit, conflict, and write failures leave the
active snapshot unchanged. The runtime uses the same ordering to publish its
default provider and model together.

Each `Complete` or `Stream` invocation captures the current directory and
defaults once, before entering the interceptor chain. Interceptor changes to
provider or model names resolve within that captured directory. An in-flight
request completes using its captured entry while later invocations see
the new snapshot. `ResolveRequest` uses the current directory and defaults for
its own call; the returned names do not pin callbacks across later calls.

Explicit provider and model fields override their respective defaults. An
empty default provider automatically selects the only available provider. With
multiple providers and no configured default, requests must specify a
provider. Removing or renaming the default provider makes implicit selection
fail until it is configured again; explicit valid requests and configuration
remain available. Invalid or duplicate directory entries require repair before
the directory can be used.

Provider choices in `model-runtime`, `agent-default`, and `context-compact`
use current source snapshots. `usage-default` estimates input tokens with
`unicode-estimate-v1` for every resolved model and has no provider route
configuration. Non-text content is skipped by this estimate. Existing forms
remain immutable; closed choices are revalidated
at submission. Model
and reasoning-effort choices in `model-runtime` use provider capability
directories, while changes to other plugins' configuration are not applied
live. Existing explicit agent or context provider or model overrides continue
to take precedence over runtime defaults.

## Configuration boundaries

Model configuration through the existing Operations needs no restart. This
covers already-loaded plugins. Installing new plugin binaries follows the
existing runtime image build and startup workflow. Editing state files directly
does not trigger live reload. Provider state file formats and names are
unchanged. Model enumeration and a new model-picker UI are outside this change.

## Verification

Coverage includes initial empty configuration, newly added providers,
immediate default changes, provider replacement during complete and streaming
calls, cross-source name conflicts and recovery, disappeared defaults,
revalidation of stale forms, secret preservation, and failed persistence or
conflict leaving active state unchanged. Concurrent publication and invocation
must also pass the Go race detector.

## Local SDK verification

Verify the SDK checkout and all six affected plugins together from the plugins
repository:

```sh
tools/test-local-sdk.sh /absolute/path/to/sdk
```

The script creates a temporary workspace containing that SDK checkout and the
six plugin modules, runs `go vet ./...` and `go test -race ./...` in each module,
and removes its workspace afterward. It preserves the repository's `go.work`
and module dependency files. Caller settings such as `GOCACHE` remain in effect.
No Core checkout or build is required.

Five of these model-related plugins declare SDK `v0.2.10`; `agent-default`
now declares `v0.2.11` for its child-agent contracts. Check each `go.mod` and
the selected module graph: a composition uses one SDK version, and all modules
must pass independent checks with `GOWORK=off`.
The temporary workspace is used when testing further SDK changes from
a local checkout. It leaves dependency files unchanged; do not add local
`replace` directives or external SDK paths to the repository's `go.work`.

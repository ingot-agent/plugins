# Developing a plugin

A plugin is an ordinary Go module with `ingot.plugin.toml` at its module root.
Core discovers and validates the manifest, resolves a static Component Graph,
generates ordinary constructor calls, and compiles the graph into a Runtime
Image. Runtime startup does not rediscover or dynamically load plugin files.

For a complete implementation, follow the [first-plugin tutorial](tutorials/first-plugin.md):
write a working SDK tool, test it independently, build its manifest into an Image,
and invoke it through `tool.Runtime` without an API key. The [complete recipes](recipes.md)
cover browser chat, file editing with approval, and child agents.

## Repository and identity

Official modules live in first-level directories, use module path
`github.com/ingot-agent/plugins/<directory>`, and include `go.mod`,
`ingot.plugin.toml`, `CHANGELOG.md` and a current `README.md`. Third-party plugins
use their own module path and follow the same Builder contract. They do not
need to join this repository or import the agent SDK.

The module path is distribution identity; manifest `name` is the short plugin
name used for CLI references and state scope. Component names are local to the
plugin. Operation groups are optional presentation labels. Do not use these
different identifiers interchangeably.

## Manifest and constructor

This minimal manifest declares one component at the module root:

```toml
manifest_version = 1
name = "example.service"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

`config_package` remains required in manifest v1. It locates a root Go package;
it does **not** request generated configuration injection. Every component
package declares named `Dependencies` and `Exports` structs and precisely:

```go
package example

import (
    "context"
    ingotabi "github.com/ingot-agent/ingot-abi"
)

type Dependencies struct{}
type Exports struct{}

func New(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
    return Exports{}, nil, nil
}
```

This is a construction skeleton, not a complete agent or application. Add the
capabilities the component actually provides and consumes. There is no `Config`
argument; the older three-argument signature is rejected by the current Builder.
Use the ABI version supported by Core (currently `v0.1.0`) and check the
[current manifest reference](https://github.com/ingot-agent/ingot/blob/main/docs/FILE_FORMATS.md).

## Typed capabilities

Put reusable contracts in an independent package, such as the
[agent SDK](https://github.com/ingot-agent/sdk) or another domain contract module.
Do not put capability target types in a graph component's implementation package.
Official plugin modules cannot import sibling plugin implementations or Core.

| Dependency field | Resolution |
|---|---|
| `T` | Exactly one matching provider |
| `ingotabi.Optional[T]` | Zero or one; multiple matches are ambiguous |
| `[]T` | Ordered collection of matching providers |

ABI `Named[T]` can retain provider identity where that contract is needed.
Missing required capabilities, ambiguous providers, self-dependencies and
cycles fail graph construction. Order follows the resolved graph and declared
plugin/component/export ordering. Do not use package globals or string-based
service lookup to evade it.

The exact ABI types `invocation.Invocation`, `lifecycle.Controller` and
`state.Scope` are injected by the generated runtime and cannot be exported by
plugins. Their semantics are documented in the [ABI repository](https://github.com/ingot-agent/ingot-abi).
They are not ordinary replaceable agent services.

## Lifecycle, state and configuration

`New` must return promptly. A component owning background work starts it with
instance-owned cancellation and returns Cleanup that stops and joins it.
Cleanup runs in reverse construction order. Use `lifecycle.Controller` to
request process shutdown; do not terminate the process with `os.Exit`.
Observe `invocation.ModeCheck`: validate construction without starting user
loops/listeners or retaining external resources after cleanup.

Inject `state.Scope` when persistent state is required. All components of one
plugin share `<runtime-home>/state/<manifest-name>/`. The plugin owns decoding,
defaults, validation, schema migration and persistence. There is no global
runtime `config.toml`, implicit configuration registry or Builder secret store.
Allow missing initial settings so users can reach the configuration operation;
validate configuration when the corresponding capability is used as needed.

Expose external configuration through `operation.Operation` and call-scoped
`interaction.Channel`. Current official `config` Operations start with `{}` and
collect structured values through Interaction. Follow the
[configuration conventions](plugin-configuration-interaction-conventions.md):
validate a candidate, reject stale writes, persist atomically, then publish live
state or accurately report restart requirements. Do not retain the invocation's
Interaction channel after `Invoke` returns. Reusable cross-plugin behavior must
use typed capabilities, not dispatch another plugin's operation by name.

The manifest's optional `[state]` compatibility declaration does not implement
state migration. Document migrations and rollback limits beside the code.

## Invocation context and live sources

Carry business routing through public envelopes such as `tool.Invocation` and
`execution.Scope`. Context is for cancellation/deadlines and non-authoritative
observation correlation. Bind Session-scoped effects with the injected
`interaction.ExecutionBinder`; do not recover Session identity from hidden
context values or ambient current-workspace globals.

Model adapters export `model.ProviderSource`, which returns caller-owned
snapshots of named invocation functions. The graph remains fixed while the
source publishes new configured providers. See the
[provider contract](live-model-provider-configuration.md) for callback lifetime,
concurrency and duplicate-name rules. Static graphs do not imply that every
capability's configuration is frozen until restart.

## Local composition and verification

Run module checks in isolation with the same settings as CI:

```sh
GOWORK=off go mod tidy -diff
GOWORK=off go vet ./...
GOWORK=off go test -race ./...
```

On PowerShell, set `$env:GOWORK = 'off'` first and run the commands without the
inline assignments. Check the repository as described in [CONTRIBUTING.md](../CONTRIBUTING.md).

To exercise a changed plugin in a Core project, add the module directory as a
local source, inspect resolution, then build/start the desired Runtime:

```sh
ingot plugin add ../plugins/tool-edit
ingot project resolve
ingot up -d -- web
```

Adapt the path to the actual checkout. Local sources may require other locally
developed contracts; use an explicitly isolated development workspace for those
checks. Never publish local replacements or assume workspace success proves
released dependencies work. Core integration is additional validation; this
repository's normal CI does not check out or build Core.

Test public behavior: nil/invalid dependencies, cancellation, concurrency,
ownership, persistence failures, conflicts, required host effects and schema
errors. Update the module README and changelog for observable changes. Run
browser/embedded-asset checks when modifying WebUI, and follow
[RELEASE.md](../RELEASE.md) before releasing a module tag.

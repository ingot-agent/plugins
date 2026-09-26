# Configuring official plugins

Configuration is **Plugin-owned state**. The Builder connects implementations;
it does not decode a global runtime `config.toml`, inject a `Config` constructor
argument, or provide a secret store.

## Match the installed plugin version

The activation matrix and module-group commands below describe the current
plugin source. Core's shipped profiles still pin plugin `v0.1.0`, whose model
configuration and Operation names differ:

| Installed plugins | Browser configuration Operations | Activation |
|---|---|---|
| Profile's released `v0.1.0` modules | Group `configuration`, names `model.openai-compatible.config`, `model.runtime.config`, `agent.default.config`, `app.backend.config` | Follow `restart_required`; provider changes require restart to rebuild the provider directory |
| Current checkout described by this branch | `/model-openai-compatible config`, `/model-runtime config`, `/agent-default config`, `/app-webui config` | The model/agent Operations publish live settings; changed WebUI server settings require restart |

For a new `v0.1.0` Runtime, configure **one provider first**, run
`ingot restart <runtime>`, then configure the model-runtime default provider and
model, and restart again when requested. Set an explicit default before adding
more providers: the older runtime can fail startup with multiple providers and
no default. Discover the actual Operation definitions in the browser rather
than assuming names or activation behavior from another module version.
Use the configuration Operation's `restart_required` result here. Core's
`runtime show` flag describes Image/binding drift and does not detect whether
a plugin has saved settings that require restarting its existing Image.

## First run with current plugin source

Follow the [Core setup guide](https://github.com/ingot-agent/ingot/blob/main/docs/USAGE.md),
start with `ingot up -d -- web`, and open `http://127.0.0.1:7316/`.
Use the composer's slash-command menu:

1. `/model-openai-compatible config`: add a named provider, API base URL and
   credentials. See the [adapter reference](../model-openai-compatible/README.md)
   for URL handling and supported requests.
2. `/model-runtime config`: choose the default provider and model identifier.
   Providers are discovered from live sources; model identifiers are not
   enumerated. Use an identifier supported by the provider.
3. `/agent-default config`: optionally override model selection, generation
   settings and the round limit. Explicit agent values take precedence over
   model-runtime defaults; clear old overrides when switching models.
4. Create/select a Session with the intended workspace and send a message.
   A workspace binding does not sandbox local tools.

These Operations have local name `config` and a Group matching the hyphenated
module directory. `app.backend` is a manifest name; its displayed command is
`/app-webui config`. That command configures the HTTP host, not model providers.

Each configuration Operation starts with `{}` and requests values through its
call-scoped `interaction.Channel`. Do not send TOML settings as initial input.
For HTTP integrations, discover and invoke an Operation **ID**, then answer
its Interaction Requests. Groups and command labels are presentation hints,
not routing identities. See [app-webui](../app-webui/README.md) for endpoints
and request envelopes.

## State and file ownership

A managed Runtime has a home at `INGOT_HOME/runtimes/<name>/`. The generated
runtime assigns an absolute `state.Scope.Dir()` under its `state/` directory
to each plugin at `state/<manifest-name>/`; its components share that scope.
For `app-webui`, the manifest name is `app.backend`, so the directory is
`state/app.backend/`.

Most configurable plugins use `config.toml` in that scope. README examples are
the **contents of that plugin's file**, without `[plugins.<name>]` wrappers.
Additional or distinct state includes:

| Module | State / configuration |
|---|---|
| [agent-default](../agent-default/README.md) | `subagents.toml` overrides the built-in `coder`/`explorer`/`reviewer` child types available when `tool-subagent` and child storage/workspace capabilities are installed; scheduler limits are implementation constants |
| [session-sqlite](../session-sqlite/README.md) | SQLite sessions/workspaces/metadata; no `config` Operation |
| [skill-runtime](../skill-runtime/README.md) | Skills in the plugin scope, plus the embedded built-in skill; no `config` Operation |
| [tool-subagent](../tool-subagent/README.md) | Injected child-management capability; no private configuration Operation |

Use Operations for validated, coherent configuration updates. Direct edits to
`config.toml` or `subagents.toml` do not trigger live reload: stop the Runtime
for maintenance edits and start it afterward. Skill files are different:
`skill-runtime` refreshes its catalog during prompt contribution, read/add and
status calls, as described in its README.
Back up state before migration. Image rollback changes the executable binding;
it does not restore state or undo external tool effects.

## Activation boundaries

The current catalog has **19 modules, 16 with configuration Operations**:

| Activation | Modules / settings |
|---|---|
| Saved changes apply to later calls without restart | `agent-default`, `asset-local`, `context-compact`, `http-default`, `interceptor-approval`, `interceptor-script`, `model-openai-compatible`, `model-openai-responses`, `model-runtime`, `prompt-default`, `tool-ask`, `tool-edit`, `tool-runtime`, `tool-shell`, `usage-default` |
| Changed settings require restart | `app-webui`: listener, replay/subscriber buffers, heartbeat, Operation retention and asset-upload limit |
| Loaded separately at construction | `agent-default` child definitions in `subagents.toml`; not covered by `/agent-default config` |

The 15 live Operations return `restart_required:false` after persistence and
publication. Existing calls keep captured settings; later calls use the update.
WebUI compares saved values with startup values and reports the actual
`restart_required`. After changing the host, run `ingot restart` and open the
new address if it changed.

See [live configuration](live-plugin-configuration.md) for transactions and
[live model providers](live-model-provider-configuration.md) for provider
snapshots, conflicting names and defaults. Adding implementations still
requires a recipe change and image build; live settings do not add graph nodes.

## Credentials and troubleshooting

Sensitive fields do not expose saved secrets as defaults. Where supported,
explicit keep/replace/clear choices control updates. Sensitive presentation
does not encrypt state files: protect Runtime homes with filesystem access
controls and keep populated state out of source control.

| Symptom | Check |
|---|---|
| No model available | Configure the loaded adapter, then model-runtime; an unconfigured source can legitimately be empty |
| Provider ambiguous or duplicated | Use unique provider names across adapters and select a default when needed |
| Model switch ineffective | Check agent/context overrides and in-flight requests retaining snapshots |
| Command absent | Confirm that module is in the image; default profiles do not include all modules |
| WebUI setting inactive | Inspect `restart_required` and restart the Runtime |
| Configuration conflict | Reopen the form; stale writes are rejected instead of overwriting newer state |
| Manual TOML ignored or rejected | Check Runtime/plugin scope, documented fields and restart |

Bug reports should include module versions and redacted reproductions rather
than copies of populated Runtime homes.

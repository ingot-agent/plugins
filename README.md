# Ingot plugins

Official, independently versioned Go plugins for [Ingot](https://github.com/ingot-agent/ingot).
Ingot resolves typed dependencies at build time and compiles the selected
composition into one native Runtime Image. Each first-level plugin directory is
an independent Go module, subject to the same Builder rules as third-party plugins.

[Documentation](docs/README.md) · [Configuration](docs/configuration.md) ·
[Plugin development](docs/plugin-development.md) · [Contributing](CONTRIBUTING.md) ·
[First plugin tutorial](docs/tutorials/first-plugin.md) ·
[Complete recipes](docs/recipes.md) · [Security](SECURITY.md) · [Releases](RELEASE.md)

## Start using the plugins

Install Core using its [installation guide](https://github.com/ingot-agent/ingot/blob/main/docs/USAGE.md).
Building an image requires Go 1.24.2 or newer; running an already-built image
requires neither Go nor this checkout.

```sh
ingot setup
ingot init .
ingot up -d -- web
# Open http://127.0.0.1:7316/
```

The shipped `v0.1.0` profiles use configuration Operations named
`model.openai-compatible.config` and `model.runtime.config`, under the
`configuration` group. Add one provider, run `ingot restart`, configure the
default provider/model, then restart again when requested. Current plugin
source uses `/model-openai-compatible config` and `/model-runtime config` with
live activation instead. See the version-specific
[configuration walkthrough](docs/configuration.md) and [browser host reference](app-webui/README.md).

Core's default profile and this repository's [collection](collections.toml)
select **11 modules at exact `v0.1.0` versions**, not every module in this
checkout. They include the browser, shell and question tools, but omit file-editing,
subagent and skill tools, interceptors, context compaction, usage counting and
Responses. Core's minimal profile selects 9 modules, omits shell and question
tools, and also uses the browser. Read the selected tag's documentation when
using a release: current branch behavior may not exist in those pinned versions.

Add an additional module by its module path with an available exact version,
or a local module directory during development, then rebuild. For example,
from a project beside a `plugins` checkout:

```sh
ingot plugin add ../plugins/tool-edit
ingot up -d -- web
```

The Builder validates the complete graph. New plugins may need additional
capability providers; duplicate providers of singular capabilities can make
a graph ambiguous. Replacing implementations requires rebuilding the image;
[documented live settings](docs/live-plugin-configuration.md) apply to an
already-loaded instance without rebuilding.

## Plugin catalog

Module paths start with `github.com/ingot-agent/plugins/`. Directory names,
manifest names and Operation groups are separate identifiers; `app-webui`
declares **`app.backend`**. Each README describes current components,
dependencies, operations/tools, state, defaults and limitations.

| Module directory | Manifest name | Purpose |
|---|---|---|
| [agent-default](agent-default/README.md) | `agent.default` | Agent loop, accounting, observations and single-Turn children |
| [app-webui](app-webui/README.md) | `app.backend` | Browser workspace, HTTP/SSE, Interaction and Operation host |
| [asset-local](asset-local/README.md) | `asset.local` | Immutable local binary assets |
| [context-compact](context-compact/README.md) | `context.compact` | Context summaries and checkpoints |
| [http-default](http-default/README.md) | `http.default` | Shared HTTP transport |
| [interceptor-approval](interceptor-approval/README.md) | `interceptor.approval` | Tool allow/ask/deny policy |
| [interceptor-script](interceptor-script/README.md) | `interceptor.script` | External script hooks for typed interception chains |
| [model-openai-compatible](model-openai-compatible/README.md) | `model.openai-compatible` | Chat Completions adapter |
| [model-openai-responses](model-openai-responses/README.md) | `model.openai-responses` | Responses API adapter |
| [model-runtime](model-runtime/README.md) | `model.runtime` | Provider selection, defaults and interception |
| [prompt-default](prompt-default/README.md) | `prompt.default` | Ordered prompt rendering |
| [session-sqlite](session-sqlite/README.md) | `session.sqlite` | Sessions, history, metadata, workspaces and child persistence |
| [skill-runtime](skill-runtime/README.md) | `skill.runtime` | Plugin-owned skills, prompting and skill tools |
| [tool-ask](tool-ask/README.md) | `tool.ask` | Structured questions through Interaction |
| [tool-edit](tool-edit/README.md) | `tool.edit` | File editing, reading and searching |
| [tool-runtime](tool-runtime/README.md) | `tool.runtime` | Lookup, JSON schema validation and interception |
| [tool-shell](tool-shell/README.md) | `tool.shell` | Session-workspace shell execution |
| [tool-subagent](tool-subagent/README.md) | `tool.subagent` | Child-agent discovery, creation and control |
| [usage-default](usage-default/README.md) | `usage.default` | Unicode input-token estimation and accuracy reporting |

## Repository boundary and validation

Plugin modules cannot depend on Core or sibling plugin implementations, or
contain `go.mod` replacements. Shared contracts belong in the optional
[SDK](https://github.com/ingot-agent/sdk), fixed [ABI](https://github.com/ingot-agent/ingot-abi),
or another contract module. `go.work` may list only first-level plugins here;
CI uses `GOWORK=off` to verify each changed module independently.

```sh
python scripts/validate_repo.py
python -m unittest discover -s scripts/tests -p "test_*.py"
```

Python 3.11+ is required (CI uses 3.12). CI runs `go mod tidy -diff`,
`go vet ./...` and `go test -race ./...` for each changed plugin. Repository-only
changes have an explicit no-op plugin job. The workflow does not run browser
jobs, build Core, or publish releases. See [contribution](CONTRIBUTING.md) and
[release instructions](RELEASE.md) for additional frontend checks.

## License

[MIT](LICENSE). Bundled third-party assets retain their notices, including the
[tokenizer assets](usage-default/assets/README.md).

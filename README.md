# Ingot plugins

Official plugins for the Ingot agent runtime.

This repository contains independently maintained, first-level plugin modules
that can be validated and tested without checking out or building Ingot Core.

## Repository boundary

- Plugin source lives in one first-level directory per plugin.
- Each plugin is an independent Go module.
- The repository does not vendor, checkout, or build `ingot-agent/ingot`.
- Plugins must not depend on Ingot Core or another plugin implementation.
- WebUI-specific CI and release automation are intentionally out of scope for
  this initial repository skeleton.

## Layout

```text
.
├── .github/
│   ├── pull_request_template.md
│   └── workflows/ci.yml
├── scripts/detect_changed_plugins.py
├── scripts/tests/test_detect_changed_plugins.py
├── scripts/tests/test_validate_repo.py
├── scripts/validate_repo.py
├── CONTRIBUTING.md
├── RELEASE.md
└── go.work
```

`go.work` declares `go 1.24.2` and may contain `use` entries for current
first-level plugin modules, such as `./tool-shell`. It must not reference
external, nested, parent, Core, or other non-plugin paths. Plugin checks run
with `GOWORK=off` so every module is still tested in isolation and the
workspace never becomes a release dependency boundary.

The explicitly allowed infrastructure directories are `.github/`, `scripts/`,
`tools/`, and `docs/`. A different first-level directory is recognized as a
plugin only when it contains both `go.mod` and `ingot.plugin.toml`; otherwise
repository validation rejects it as an unknown top-level directory.

## Plugin inventory

- Agent and application: `agent-default`, `app-webui`.
- Runtime services: `asset-local`, `context-compact`, `http-default`,
  `prompt-default`, `session-sqlite`, `usage-default`.
- Models: `model-runtime`, `model-openai-compatible`,
  `model-openai-responses`.
- Interceptors: `interceptor-approval`, `interceptor-script`.
- Tools: `tool-ask`, `tool-edit`, `tool-runtime`, `tool-shell`, and
  `tool-subagent` for single-Turn child-agent discovery and management.

## Adding a plugin

Create a new first-level directory containing:

```text
<plugin-dir>/
├── go.mod
├── ingot.plugin.toml
└── CHANGELOG.md
```

The module path must be
`github.com/ingot-agent/plugins/<plugin-dir>`, and `ingot.plugin.toml` must
define a unique, non-empty top-level `name`.

Run repository validation from the repository root:

```bash
python scripts/validate_repo.py
```

For pull requests and pushes to `main`, CI detects which first-level plugin
directories changed and creates one parallel matrix job per changed plugin.
Each matrix job runs `go mod tidy -diff`, `go vet ./...`, and
`go test -race ./...` with `GOWORK=off`. If no plugin changed, the matrix runs
a single explicit no-op entry so the Plugin Tests job still passes visibly.

## SDK and ABI compatibility

The current compatibility contract is compile compatibility against the exact
SDK and ABI versions declared by each plugin's `go.mod`. Successful
`go vet ./...` and `go test -race ./...` demonstrate that the plugin builds
and tests against those declared versions.

CI does not currently validate manifest compatibility ranges, ABI schemas, or
cross-repository version matrices. Those checks belong to a later explicit
compatibility-validation phase.

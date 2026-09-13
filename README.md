# Ingot plugins

Official plugins for the Ingot agent runtime.

This repository is currently bootstrapped with no plugin modules. It is the
home for independently maintained, first-level plugin modules that can be
validated and tested without checking out or building Ingot Core.

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
├── scripts/validate_repo.py
├── CONTRIBUTING.md
├── RELEASE.md
└── go.work
```

`go.work` currently declares only `go 1.24.2`; it does not use any local
modules. Plugin checks run with `GOWORK=off` so every module is tested in
isolation.

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

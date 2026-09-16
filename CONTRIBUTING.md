# Contributing

Thanks for contributing to the official Ingot plugins repository.

## Repository rules

Each plugin must be a first-level directory and an independent Go module. It
must contain:

- `go.mod`
- `ingot.plugin.toml`
- `CHANGELOG.md`

Official plugins that expose configuration through Operation and Interaction
must follow [`docs/plugin-configuration-interaction-conventions.md`](docs/plugin-configuration-interaction-conventions.md).
The repository-wide baseline review is recorded in
[`docs/official-plugin-configuration-audit-2026-09-16.md`](docs/official-plugin-configuration-audit-2026-09-16.md).

The module path must exactly match
`github.com/ingot-agent/plugins/<plugin-directory>`. The manifest must contain
a unique, non-empty top-level `name`.

Plugin modules must not:

- contain a `go.mod` `replace` directive;
- depend on `github.com/ingot-agent/ingot` or any of its submodules; or
- depend on another first-level plugin module's implementation.

The repository intentionally has no Core checkout, Core build, WebUI-specific
CI, or release workflow. Keep plugin validation independent from those
systems.

## Repository layout and workspace

The allowed infrastructure directories are `.github/`, `scripts/`, `tools/`,
and `docs/`. Every other first-level directory must be identifiable as a
plugin by containing both `go.mod` and `ingot.plugin.toml`. Additions such as
`examples/` or `testdata/` require an explicit repository-layout policy change
instead of being silently ignored.

The root `go.work` may contain `use` entries, but every entry must have the
exact form `./<plugin-directory>` and reference a current first-level plugin
module. External paths, parent paths, nested paths, Core paths, and duplicate
entries are rejected. CI and release checks continue to use `GOWORK=off`.

## Local checks

From the repository root, run:

```bash
python scripts/validate_repo.py
```

For each changed plugin directory, run the same checks used by CI:

```bash
cd <plugin-directory>
GOWORK=off go mod tidy -diff
GOWORK=off go vet ./...
GOWORK=off go test -race ./...
```

CI detects changed first-level plugin directories from the pull request or
push diff and runs these commands in a separate matrix job for each one.
Repository-only changes produce a visible no-op Plugin Tests matrix entry.
The changed-plugin selection and matrix construction helpers are covered by
standard-library unit tests in `scripts/tests/`.

At this stage, SDK and ABI compatibility means that the plugin compiles and
passes tests against the exact SDK and ABI versions in its `go.mod`. Manifest
compatibility ranges, ABI schema checks, and cross-repository compatibility
matrices are not part of this CI layer yet.

Pull requests should use squash merge into `main`. Keep changes focused on
the plugin being changed and update its `CHANGELOG.md` when behavior or
packaging changes.

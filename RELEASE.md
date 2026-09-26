# Release process

The current repository has CI but no release workflow. Releases are prepared
manually and reviewed in a pull request. Each plugin is a separate Go module;
a Core release or another plugin's tag does not release this module.

## Before a release

1. Confirm the plugin's `go.mod`, `ingot.plugin.toml`, and `CHANGELOG.md` are
   present and consistent.
2. Run `python scripts/validate_repo.py` from the repository root.
3. Run the isolated checks from `CONTRIBUTING.md` with `GOWORK=off`.
4. For `app-webui`, run `npm ci`, `npm run lint`, `npm test`,
   `npm run check:dist`, and `npm run test:e2e` from `app-webui/web` so the
   generated assets and Go embed are released together.
5. Update the plugin changelog with the release version and user-visible
   changes.
6. Review the module README against the release commit: required providers,
   tool/Operation schemas, state defaults and migrations, live/restart behavior,
   and supported platform/provider limitations. Check links and bundled
   third-party license notices. Historical designs are not acceptance evidence.
7. Check the exact SDK/ABI versions in the module's `go.mod` without local
   workspace replacements. If a new contract is required, release that contract
   first and update the module dependency before tagging the plugin.
8. Verify intended compositions as well as isolated module tests. In particular,
   check the Core profile and this repository's `collections.toml` separately:
   both currently pin `v0.1.0` modules and do not automatically follow branch
   changes or new plugin tags. Updating those recipes requires explicit review.
   [Complete local recipes](docs/recipes.md) provide three graph/build baselines
   and business acceptance steps. Their local SDK/ABI replacements must be
   removed for a released-input check: execute from an isolated directory with
   no ancestor `go.work`, use actual exact module versions, and verify the lock
   has no development replacements. Setting `GOWORK=off` alone does not prevent
   the Builder from discovering workspace replacements itself.
9. When editing the [first-plugin tutorial](docs/tutorials/first-plugin.md), extract
   the complete files named beside its code blocks into a temporary directory,
   run the documented independent checks, then build and run its demonstration
   host. Preserve the expected greeting, Schema rejection and normal exit.

## Tagging

Releases are per plugin. Use a plugin-scoped Go module tag in the form
`<plugin-directory>/vX.Y.Z` after the release pull request has been merged.
No repository tag ruleset or release workflow is defined yet.

For example, a release of the `tool-shell` module uses a tag such as
`tool-shell/vX.Y.Z`, while consumers request
`github.com/ingot-agent/plugins/tool-shell@vX.Y.Z`. Substitute the actual release
version; do not create a repository-wide `vX.Y.Z` tag for a nested module.
After publishing, verify that the tag can be resolved through the intended Go
module proxy and that the documented composition builds from released inputs.

Keep release notes focused on the plugin being released. Do not bundle Core or
sibling-plugin implementation changes into a plugin release.

# Release process

Release automation is intentionally not part of the initial repository
skeleton. Until it is added, releases are prepared manually and reviewed in a
pull request.

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

## Tagging

Releases are per plugin. Use a plugin-scoped Go module tag in the form
`<plugin-directory>/vX.Y.Z` after the release pull request has been merged.
No repository tag ruleset or release workflow is defined yet.

Keep release notes focused on the plugin being released. Do not bundle Core or
sibling-plugin implementation changes into a plugin release.

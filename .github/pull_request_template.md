## Summary

<!-- What does this pull request change? -->

## Plugin directories changed

<!-- List each first-level plugin directory touched by this pull request. -->

- <!-- Add a plugin directory name here. -->

## Validation

- [ ] `python scripts/validate_repo.py`
- [ ] For every affected plugin module: `GOWORK=off go mod tidy -diff`
- [ ] For every affected plugin module: `GOWORK=off go vet ./...`
- [ ] For every affected plugin module: `GOWORK=off go test -race ./...`

## Checklist

- [ ] Each plugin has `go.mod`, `ingot.plugin.toml`, and `CHANGELOG.md`.
- [ ] Each module path is `github.com/ingot-agent/plugins/<directory>`.
- [ ] No `go.mod` uses `replace`.
- [ ] No plugin depends on Ingot Core or another plugin implementation.
- [ ] No unrelated plugin source or WebUI changes are included.

# Changelog

All notable changes to this plugin are documented in this file.

## 0.1.0 - 2026-11-24

### Build

- Require Go 1.26.0 or newer. Go 1.24 and Go 1.25 are both out of support, so
  the previous 1.24 floor advertised a toolchain line that no longer receives
  security fixes. This is a build requirement only; no contract, configuration
  or runtime behavior changed.

- Require Go 1.26.0 or newer. Go 1.24 and Go 1.25 are both out of support, so
  the previous 1.24 floor advertised a toolchain line that no longer receives
  security fixes. This is a build requirement only; no contract, configuration
  or runtime behavior changed.

### Added

- Add `web_search` backed by the DeepSeek Search API, including query
  operators, result metadata and optional page content.
- Add `web_fetch` backed by the DeepSeek Fetch API, returning page Markdown.
- Add a live `config` Operation for the API key, base URL, request timeout and
  response-size limit, with `DEEPSEEK_API_KEY` as an environment fallback.
- Retry DeepSeek `408`, `429`, `5xx` and transport failures, honor
  `Retry-After`, and map API failures to tool business results.

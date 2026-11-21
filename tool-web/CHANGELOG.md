# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Added

- Add `web_search` backed by the DeepSeek Search API, including query
  operators, result metadata and optional page content.
- Add `web_fetch` backed by the DeepSeek Fetch API, returning page Markdown.
- Add a live `config` Operation for the API key, base URL, request timeout and
  response-size limit, with `DEEPSEEK_API_KEY` as an environment fallback.
- Retry DeepSeek `408`, `429`, `5xx` and transport failures, honor
  `Retry-After`, and map API failures to tool business results.

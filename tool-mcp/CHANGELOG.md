# Changelog

## Unreleased

- Require Go 1.26, matching the repository build floor.
- Add `tool.mcp`, an MCP client gateway exporting remote MCP server tools as
  Ingot tools. Supports the `stdio` and Streamable HTTP transports, snapshot
  based tool discovery, per-server tool filtering, and the `config` and
  `refresh` configuration Operations plus the `mcp_status` tool.

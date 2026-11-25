# Changelog

## Unreleased

- Require Go 1.26, matching the repository build floor.
- Scope each MCP session to one Session and set its working directory at process
  creation from the Session workspace. Sessions no longer share server-side
  state, and relative paths resolve inside the caller's workspace instead of the
  Runtime working directory. Discovery uses a private directory under the plugin
  state scope.
- Add `tool.mcp`, an MCP client gateway exporting remote MCP server tools as
  Ingot tools. Supports the `stdio` and Streamable HTTP transports, snapshot
  based tool discovery, per-server tool filtering, and the `config` and
  `refresh` configuration Operations plus the `mcp_status` tool.

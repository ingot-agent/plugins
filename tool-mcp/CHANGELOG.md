# Changelog

## Unreleased

- Require Go 1.26, matching the repository build floor.
- Support MCP elicitation: a server can ask the Session user for input during a
  call. The capability is advertised only for a Session-bound connection and only
  when the composition provides an interaction facility, so discovery can never
  prompt. Supports form and URL mode; URL mode shows the full address as a link,
  never opens it automatically, requires an explicit confirmation and enforces an
  encrypted scheme. Completion notifications reach the owning Session as an
  event. Server-provided text is markdown-escaped before it is rendered.
- Scope each MCP session to one Session and set its working directory at process
  creation from the Session workspace. Sessions no longer share server-side
  state, and relative paths resolve inside the caller's workspace instead of the
  Runtime working directory. Discovery uses a private directory under the plugin
  state scope.
- Add `tool.mcp`, an MCP client gateway exporting remote MCP server tools as
  Ingot tools. Supports the `stdio` and Streamable HTTP transports, snapshot
  based tool discovery, per-server tool filtering, and the `config` and
  `refresh` configuration Operations plus the `mcp_status` tool.

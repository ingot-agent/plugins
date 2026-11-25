# 给 Agent 接上 MCP 工具

本教程把一个真实的 MCP server 接到 Ingot Agent 上，让模型能用它。用 Playwright MCP
做例子：接上之后，模型就能自己打开网页、点击、截图。

全程只需要**三行配置**，不需要写代码，不需要了解 MCP 协议。

## 前提

- 已经能用 `ingot up` 起一个带 WebUI 的 Agent（见
  [配置指南](../configuration.md)）。
- 目标 MCP server 能通过命令启动。多数 server 用 `npx` 或 `uvx` 分发，
  所以需要 `node`/`npx` 或 `uv` 在 PATH 里。

## 1. 把插件加进组合

`tool.mcp` 不在默认集合里，需要显式加入：

模块发布之后按版本引用：

```sh
ingot plugin add github.com/ingot-agent/plugins/tool-mcp@v0.1.0
ingot project resolve
ingot up -d -- web
```

在自己仓库里开发、或该模块还没有发布 tag 时，直接指向目录：

```sh
ingot plugin add ../plugins/tool-mcp
ingot project resolve
ingot up -d -- web
```

> **注意端口。** 如果机器上已经有一个 Runtime 占用 `127.0.0.1:7316`，新的 Runtime 会
> 启动失败（`address already in use`）。先停掉旧的，或改 `.ingot/runtimes/<名字>/state/
> app.backend/config.toml` 里的 `address`。

## 2. 告诉插件启动哪个 server

在浏览器里打开 WebUI，输入：

```
/tool-mcp config
```

弹出的表单里填一个 server。以 Playwright 为例：

| 字段 | 填什么 |
|---|---|
| `name` | `playwrite`（只能小写字母、数字、`-`、`_`） |
| `transport` | `stdio` |
| `command` | `npx` |
| `args` | 分两条填：`-y` 和 `@playwright/mcp@latest` |
| `enabled_tools` | 留空即可（默认全部） |

其余字段保持默认。**`secrets_action` 选 `Replace`**（新 server 还没有已存的值）。

保存后 Operation 会立刻连接 server、拉取工具清单，并返回：

```json
{
  "restart_required": true,
  "restart_reasons": ["the running image exports 0 tools; discovery found 25 (+25 -0)"],
  "approval_hint": ["mcp_playwrite_*"]
}
```

`restart_required: true` 是**正常的**：工具清单是镜像的一部分，新工具要重启后才生效。

## 3. 重启

```sh
ingot restart
```

再看一次 `/tool-mcp config` 或直接问模型，此时 `restart_required` 已经是 `false`。

## 4. 用起来

新建一个会话，直接说：

> 打开 https://example.com 并截图给我

模型会调用 `mcp_playwrite_browser_navigate`、`mcp_playwrite_browser_take_screenshot`。
工具名规则是 `mcp_<server名>_<工具名>`，一眼能看出是谁提供的。

**截图文件会落在当前会话的工作区里**，不是 Ingot 进程所在目录。这是有意的：每个
会话有自己的 MCP 进程，工作目录就是该会话的工作区。

## 5. 出问题时看哪里

模型说"没有这个工具"或者调用失败时：

1. 直接问模型"调用 mcp_status"。它会返回每个 server 的连接状态、导出的工具数、
   工作目录来源，以及失败原因。
2. 或者检查 `.ingot/runtimes/<名字>/state/tool.mcp/snapshot.json` 是否存在、
   `servers[].status` 是不是 `ok`。

常见情况：

| 现象 | 原因 | 处理 |
|---|---|---|
| `exports 0 tools`，`status: "error"` | 命令启动失败 | 手动跑一遍 `command` + `args`，看能否启动 |
| 工具数比预期少 | `max_tools_total`（默认 128）不够 | 表单里调大，或收窄 `enabled_tools` |
| 工具都在，但调用报"no workspace assigned" | 会话还没分配工作区 | 在 WebUI 里打开或新建会话后重试 |
| 改了配置但工具没变 | 需要重启 | `ingot restart` |

## 6. 建议：给工具加审批

`tool.mcp` 本身不做放行策略。接了会执行外部动作的 server（浏览器、shell、数据库），
建议配一条审批规则：

```
/interceptor-approval config
```

在规则里加一行 `mcp_playwrite_*` → `ask`。前缀规则会覆盖该 server 的全部工具，
不需要逐个写。

## 只读的下一步

- 接文件系统 server：`npx -y @modelcontextprotocol/server-filesystem <目录>`。
  注意它的目录由命令行参数决定，与会话工作区无关。
- 接远程 server：`transport` 选 `Streamable HTTP`，`url` 填 `https://...`。
  需要认证时在 `headers` 里写 `Authorization = "Bearer ${你的环境变量名}"`：
  `${...}` 在建立连接时才从环境变量解析，因此 `config.toml` 里只留下变量名，
  密钥本身不会被写入，也不会出现在 `mcp_status` 或日志里。

完整字段、表单模式、URL 授权、会话隔离等细节见
[tool.mcp 参考文档](../../tool-mcp/README.md)。

返回 [插件文档索引](../README.md)。

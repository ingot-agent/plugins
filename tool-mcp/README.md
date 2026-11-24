# tool.mcp

`tool.mcp` 是 MCP（Model Context Protocol）客户端网关插件。它连接外部 MCP
server，把对方的工具导出为普通 Ingot 工具，让模型通过标准 `tool.Runtime`
调用。模块路径为 `github.com/ingot-agent/plugins/tool-mcp`，插件 ID 为
`tool.mcp`，组件为 `default`（包 `.`），兼容 Ingot `>=0.3.0 <0.4.0`，见
[manifest](ingot.plugin.toml)。

它**不是** MCP server：不会把 Ingot 的工具暴露给外部客户端。它也不实现
MCP 的 resources、prompts、sampling、elicitation 与 roots。

## 可选依赖

| 依赖 | 必需 | 说明 |
| --- | --- | --- |
| `state.Scope` | 是 | 保存配置与工具快照 |
| `httpx.Client` | 否 | 仅 `http` transport 使用；未提供时使用插件自带的 `http.Transport`。只配置 stdio server 的组合不需要 `http.default` |
| `logging.Logger` | 否 | 连接、发现与降级诊断；缺失时静默 |
| `observation.Consumer` | 否 | 把 MCP progress 通知转成 `observation.ToolProgress` |

导出 `[]tool.Tool` 与 `[]operation.Operation`。工具要接到
[tool.runtime](../tool-runtime/README.md) 才能被模型调用；审批由
[interceptor.approval](../interceptor-approval/README.md) 负责，本插件不含自己的
放行策略。

## 工具集是构造期快照

标准 `tool.runtime` 在构造时冻结工具定义，因此本插件不尝试热更新工具列表：

1. `config` 或 `refresh` Operation 连接 MCP server，执行 `tools/list`，把归一化
   结果写入快照文件。
2. 构造插件时只读取快照，不建连、不起进程、不起后台协程。
3. 快照里的工具集合发生变化时，Operation 返回 `restart_required: true`；
   重启 Runtime Image 后才生效。

这样保留了按工具名的审批规则、入参 JSON Schema 预校验，以及工具级可观测性；
代价是新增 server 或工具变化需要重启。

## 配置

插件读取自己 Runtime state scope 下的 `config.toml`；没有文件时没有任何 server。
文件直接包含以下配置，不加 `[plugins."tool.mcp"]` 外层表：

```toml
snapshot_version = 1
tool_name_prefix = "mcp"
expose_status_tool = true
max_result_bytes = 262144
max_tools_total = 128

[[servers]]
name = "github"
transport = "stdio"
enabled = true
command = "npx"
args = ["-y", "@modelcontextprotocol/server-github"]
env = { GITHUB_PERSONAL_ACCESS_TOKEN = "${GITHUB_TOKEN}" }
cwd = ""
inherit_env = []

enabled_tools = ["*"]
disabled_tools = []
max_concurrent_calls = 4
connect_timeout_seconds = 30
call_timeout_seconds = 120
shutdown_timeout_seconds = 5
idle_timeout_seconds = 0

[[servers]]
name = "remote"
transport = "http"
url = "https://mcp.example.com/mcp"
headers = { Authorization = "Bearer ${MCP_TOKEN}" }
enabled_tools = ["search_*"]
```

| 字段 | 默认值 | 语义 |
| --- | --- | --- |
| `snapshot_version` | `1` | 配置格式版本；未知值被拒绝 |
| `tool_name_prefix` | `mcp` | 导出工具名前缀，`^[a-z][a-z0-9]{0,15}$` |
| `expose_status_tool` | `true` | 是否导出 `mcp_status` |
| `max_result_bytes` | `262144` | 单次工具结果映射上限，范围 `[1024, 16777216]` |
| `max_tools_total` | `128` | 所有 server 合计的工具上限，范围 `[1, 1024]` |
| `servers[].name` | 无 | 服务器标识，`^[a-z][a-z0-9_-]{0,31}$`，必须唯一 |
| `servers[].transport` | `stdio` | `stdio` 或 `http` |
| `servers[].enabled` | `true` | 是否参与发现与调用 |
| `servers[].command` | 无 | stdio 可执行文件，必填 |
| `servers[].args` | 空 | 参数列表 |
| `servers[].env` | 空 | 追加给子进程的 `KEY=VALUE`，值可用 `${VAR}` |
| `servers[].cwd` | 空 | 子进程工作目录，必须为绝对路径；空表示 Runtime 进程工作目录 |
| `servers[].inherit_env` | 空 | 在安全默认白名单之外额外转发的宿主变量名 |
| `servers[].url` | 无 | http endpoint，必须绝对；非 loopback 只允许 `https` |
| `servers[].headers` | 空 | 每次请求附带的 header，值可用 `${VAR}` |
| `servers[].enabled_tools` | `["*"]` | 工具名模式；结尾 `*` 表示前缀匹配 |
| `servers[].disabled_tools` | 空 | 在 `enabled_tools` 之后应用的排除模式 |
| `servers[].max_concurrent_calls` | `4` | 同时在途调用上限，范围 `[1, 64]` |
| `servers[].connect_timeout_seconds` | `30` | 建连与握手预算 |
| `servers[].call_timeout_seconds` | `120` | 单次 `tools/call` 预算 |
| `servers[].shutdown_timeout_seconds` | `5` | 关闭 stdin 后等待退出的时间 |
| `servers[].idle_timeout_seconds` | `0` | 空闲多久后断开；`0` 表示常驻 |

未知字段、非法枚举、重复名字、非 loopback 明文 HTTP、URL 内嵌凭据一律拒绝，
错误包裹 `ErrInvalidConfig`，不会回落到宽松默认值。

### 环境变量与密钥

stdio 子进程**不会**继承完整宿主环境。默认只转发 `PATH`、`HOME`、`TMPDIR`、
`TMP`、`TEMP`、`LANG`、`LC_*`、`USER`、`LOGNAME`、`SHELL`、`TERM` 以及
Windows 必需的 `SystemRoot`、`SystemDrive`、`COMSPEC`、`PATHEXT`、`WINDIR`、
`APPDATA`、`LOCALAPPDATA`、`USERPROFILE`。其余变量必须用 `inherit_env` 显式
列出，否则宿主进程里的密钥会被整体送给第三方可执行文件。

`${VAR}` 只做一次非递归展开，支持嵌入（例如
`Authorization = "Bearer ${MCP_TOKEN}"`）。引用未定义的变量会在建连时失败，
不会静默变成空字符串。

密钥只存在于 `config.toml`（文件权限 `0600`，目录 `0700`）与进程环境中：
不写入工具快照，不进入日志、错误文本或 `mcp_status` 输出，也不参与配置摘要。

## 命名与 Schema 归一化

模型可见名形如 `<prefix>_<server>_<tool>`，例如
`mcp_github_create_issue`。非法字符替换为 `_`，连续分隔符折叠，超过 64 字节时
保留前缀并追加稳定摘要；重名时追加摘要区分。生成结果必然满足
`tool.runtime` 的 `^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`。

MCP server 的 `inputSchema` 在发现阶段归一化为 Draft 2020-12：

| server 侧写法 | 处理 |
| --- | --- |
| `$schema`、`$id` | 删除（`tool.runtime` 拒绝非 2020-12 的 `$schema`） |
| `definitions` | 重命名为 `$defs` |
| `dependencies` | 拆分为 `dependentRequired` / `dependentSchemas` |
| 元组 `items: [...]` | 转为 `prefixItems` |
| draft-04 布尔型 `exclusiveMinimum/Maximum` | 转为数值形式 |
| OpenAPI `nullable: true` | 转为类型联合；无 `type` 时转为 `anyOf` |
| 布尔 schema | 转为对象 schema |

归一化后用与 `tool.runtime` 相同的校验库预编译。**编译失败的工具会被单独
拒绝并记入快照**，不会让整个 Runtime Image 启动失败；同样，快照损坏或手工改坏
时只降级为“没有 MCP 工具”，构造仍然成功，问题通过日志与 `mcp_status` 暴露。

## 连接与进程管理

- 懒连接：首次调用才建连；同一 server 的并发建连会被合并。
- 失败退避：连续失败按 1s 起指数退避、上限 60s；退避期内快速失败并回传
  剩余时间，不发起网络请求。
- 调用不自动重试：MCP 调用可能有副作用，且 `tool.Tool` 契约不保证错误时无副作用。
- 建连失败按 `connect_timeout_seconds` 立即上报：握手超时后调用方马上拿到错误，
  进程树的清理在后台完成，不让 SDK 关闭子进程的两段等待拖长模型侧延迟。
- stdio 进程放在独立进程组；正常关闭走“关 stdin → 等待 → `SIGTERM` → `SIGKILL`”，
  关闭失败时再对仍存活的进程组补一次 `SIGKILL`，避免 `npx`/`uvx` 派生的子进程变成
  孤儿；Windows 使用 `taskkill /T` 尽力而为。
- stderr 由常驻 reader 排空到 8 KiB 环形缓冲，防止管道写满导致 server 卡死；
  内容不并入工具结果，只在发现失败时以截断形式出现在 `mcp_status` 的诊断字段里。
- http 使用 Streamable HTTP；默认不建立 standalone SSE 流，也不由传输层重试。
  跨主机重定向被拒绝，事件大小受 `max_result_bytes` 约束。

## Operation

两个 Operation 都属于 Group `tool-mcp`，输入均为 `{}`。

### `config`

通过 Interaction 展示并修改全部配置：顶层开关、默认值，以及 `servers` 列表
（嵌套 object 与字符串列表）。环境变量与 header 的值**不回填**：表单只显示
`KEY=` 或纯 `${VAR}` 引用；沿用已存值时不显示明文。每个 server 有一个
`secrets_action` 选择：

- `keep`：保留该 server 已存的 `env`/`headers`，提交的对应条目被忽略；
- `replace`：用提交的条目整体替换，提交空列表即清空。

提交后依次执行：校验候选配置 → 重读磁盘检测交互期间的并发修改
（不一致返回 `ErrConfigConflict`）→ 原子写入 → 发布到后续调用 → 标记现有连接
在下次使用时重连 → 按 `refresh_now` 立即发现。重连由下一次调用触发；如果那一刻
另一个调用正在使用同一连接，它会失败，需要在下一轮重试。

输出包含 `restart_required`、`restart_reasons`、`added`、`removed`、
每个 server 的 `status`/`tools`/`rejected`/`schema_bytes`，以及
`approval_hint`（可直接粘贴到审批策略的 `mcp_<server>_*` 前缀）。
`schema_bytes / 4` 是工具集占用的粗略 token 估算，用于在配置阶段发现工具膨胀。

### `refresh`

按当前配置重新发现并写快照，返回同样的差量与 `restart_required`。用于
server 升级后预热下一次重启；不改变正在运行的实例。

## `mcp_status`

`expose_status_tool` 为真且没有同名导出工具时，本插件导出常驻工具
`mcp_status`（输入 schema 为 `{}`）。它不访问网络，报告：

- 每个 server 的 `transport`、`status`（`connected`/`backoff`/`idle`/`never`/`disabled`）、
  连续失败次数、重试等待、最后错误、累计调用数；
- 已导出工具名、被拒绝工具及原因、快照错误；
- 快照生成时间、是否陈旧、是否需要重启。

它解决了“快照为空时模型完全不知道有 MCP 这回事”的发现问题。输出不含
环境变量值、header 值或 stderr 原文。

## 结果映射

| MCP content | Ingot content |
| --- | --- |
| `text` | `KindText` |
| `image` / `audio` | base64 解码后 `KindImage` / `KindAudio` 内联 |
| `resource_link` | `KindFile` URI 引用 |
| `resource` | 文本内联，或按 MIME 内联二进制 |
| `structuredContent` | 紧凑 JSON 文本，单独标注 |

`isError: true` 是业务失败：返回 `Result` 与 `nil` error，并把错误标记写进内容，
让模型看见并自我纠正。连接失败、协议错误、超时取消才是 Go error，并包裹
`ctx.Err()`。文本按 UTF-8 边界截断；超预算的媒体替换为占位文本；截断会显式标注。

## 限制

- 工具集合变更必须重启；`mcp_status` 的 `snapshot_stale` 是唯一提示。
- 不支持 resources、prompts、sampling、elicitation、roots；客户端不声明这些能力。
- 不支持 OAuth 交互式授权，只支持静态 header（可引用环境变量）。
- 同一个 server 的连接在多个 Session 之间共享。多用户部署下，有跨调用状态的
  server 会把状态串到别的会话；需要隔离时应为不同用户配置不同的 server 实例。
- `cwd` 默认是 Runtime 进程的工作目录，而不是某个 Session 的 workspace；需要
  固定目录时显式配置绝对路径。
- 工具数量过大会显著占用上下文并干扰工具选择。GitHub 等大型 server 建议用
  `enabled_tools` 收窄，或用 `max_tools_total` 整体限流。

## 验证

在本模块目录执行：

```sh
GOWORK=off go mod tidy -diff
GOWORK=off go vet ./...
GOWORK=off go test -race ./...
```

测试不访问网络：端到端用例把测试二进制自身作为 stdio MCP server 重新执行，
覆盖发现、重启语义、文本/图片/错误映射、参数校验与快照降级；另有针对命名、
schema 归一化、环境变量白名单、退避与并发上限的表驱动用例。

返回 [插件文档索引](../docs/README.md)。

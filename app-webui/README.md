# app.backend

`app.backend` 是 Ingot 的浏览器应用，包含 Vue 3 + Tailwind CSS 前端及 HTTP/SSE 应用边界。插件目录名为 `app-webui`，Go 模块为 `github.com/ingot-agent/plugins/app-webui`，manifest ID 为 `app.backend`，配置命令 Group 为 `app-webui`；这些标识各有用途，不能互换。[manifest](ingot.plugin.toml) 声明兼容 Ingot `>=0.3.0 <0.4.0`。它是一个包含两个组件的复合插件：

- `host`（包 `./host`）依赖 ABI `state.Scope`，持有进程内的 `EventHub`，导出 `appbackend.Runtime`、全局 `interaction.Channel`、显式作用域的 `interaction.ExecutionBinder` 和 `observation.Observer`。该组件不依赖 Agent，因此 Agent 可以使用这些能力而不会在组件图中形成环。
- `app`（包 `./app`）是没有能力导出的图叶节点，持有 HTTP 服务器、Controller、运行中的 Turn，以及保留的 Operation 结果。它依赖 host 的 `appbackend.Runtime`、`agent.History`、`session.Store`、`session.Manager`、`session.Query`、`workspace.Manager`、`workspace.Resolver`，以及 ABI `invocation.Invocation`、`lifecycle.Controller` 和 `state.Scope`。相互独立且可选的 `agent.Runtime` 与 `agent.StreamingRuntime` 至少需要提供一个。`asset.Store` 是可选依赖，Operation 通过 `[]operation.Operation` 收集；应用自身另外注册 `/app-webui config`。

模块要求 Go 1.24.2，直接 SDK/ABI 版本由 [go.mod](go.mod) 固定，当前分别为 SDK `v0.2.10`、ABI `v0.1.0`。插件没有主程序，应由 Ingot Builder 组合成 Runtime Image。

## 启动 Web UI

安装当前 Ingot CLI 后，在用于运行 Agent 的项目目录执行以下命令。`ingot init` 生成项目 `plugins.toml`；官方 default/minimal profile 均包含本插件。此处不是从 plugins 仓库执行 `go build ./cmd/ingot`，CLI 源码和安装说明位于 [ingot 仓库](https://github.com/ingot-agent/ingot)。使用独立 Home 时，为每条命令添加相同的全局 `--home /absolute/path/to/home`：

```sh
ingot init
ingot build web --tag local/ingot:web
ingot runtime command set web -- web
ingot start web
```

`build web` 已创建或更新 Runtime 绑定，不需要再次 create。启动后打开默认地址 `http://127.0.0.1:7316/`，在配置命令中设置模型供应商和 Agent 模型选择，再发送消息；新插件没有配置文件时以 Unconfigured/default 状态启动。`ingot start web` 默认连接当前终端，按 `Ctrl+C` 可停止；`ingot start web -d` 后台启动并将输出写入日志文件，可用 `ingot logs web` 查看、`ingot stop web` 停止。

最后一个 `web` 是保存给 Runtime 的 default argv，用来启用启动地址提示。HTTP 监听本身由应用组件生命周期启动，不依赖 Builder 的专用 Web 命令。使用已有 Image 创建另一个 Runtime 时，语法为 `ingot runtime create another local/ingot:web -- web`，随后执行 `ingot start another`。不要同时组合另一个全局 Interaction Channel 提供者，除非组件图已经明确消除了单值依赖歧义。

前端产物通过 Go `embed` 编入 Runtime Image；运行时不需要 Node、Vite 或外部 CDN。前端源码和构建说明位于 [web/README.md](web/README.md)。开发本模块时，项目 recipe 必须引用你的本地模块修改；只修改 checkout 不会改变引用已发布版本的 recipe。重新构建前端后，从该项目执行 `ingot up web -- web`，重建、绑定并重新启动 Runtime；或依次 `ingot build web`、`ingot restart web`。已有 Image 的显式切换可使用 `ingot runtime switch web <image>`，再重启。

当前面向可信的本机单用户环境，没有登录、多租户隔离或公网部署保护。HTTP API 能创建执行、修改会话、调用配置 Operation 和响应审批；请保持回环监听，不要直接暴露至局域网或互联网。Workspace 绑定不提供文件系统或网络沙箱。

## 工作区能力

- 应用正常启动时在插件 state 目录下创建 `workspace/` 作为默认工作区，并通过
  `GET /api/state` 的 `workspace.defaultPath` 暴露其规范绝对路径。新会话没有显式选择
  目录时绑定此默认工作区；显式选择只影响当前待创建的会话，不会成为后续会话的默认值。
- Workspace Binding 在 Session 生命周期内不可修改。由旧 schema 升级而来的未绑定
  Session 在首次创建 Turn、发起带 Session scope 的 Operation 或 Fork 时自动绑定默认工作区；
  用户也可以在这些动作发生前显式选择并完成一次性绑定。
- Web 前端通过 `POST /api/workspace/select` 请求宿主打开系统目录选择器，不提供网页内目录
  浏览回退。macOS 使用 `/usr/bin/osascript` 执行 AppleScript `choose folder`；Linux 优先执行
  `zenity --file-selection --directory`，仅在找不到 Zenity 时回退到 KDialog；Windows 使用纯
  Go 代码调用 COM `IFileOpenDialog`，并限制为真实文件系统目录。用户取消统一返回
  `{ "path": null }`。
- Linux Host 必须运行在设置了 `DISPLAY` 或 `WAYLAND_DISPLAY` 的桌面会话中，并安装
  `zenity` 或 `kdialog`。SSH 和无界面服务器不会打开选择器，接口返回
  `workspace_picker_unavailable`；默认工作区仍可直接使用。
- 会话搜索、新建、重命名、归档/恢复、分叉与确认删除；正在执行时禁用生命周期变更。
- Markdown、代码高亮/复制、折叠推理、工具调用卡片和独立执行详情；Turn、Round、Model、Tool 与用量信息来自公开 SDK 能力。
- 流式输出及 Run-only 降级、停止执行、内联审批/自由输入、跨会话待处理请求抽屉。
- 拖放、选择及粘贴图片上传；历史附件按需预览或下载。Asset 能力缺失时禁用上传。
- Operation 简单表单与复杂 JSON 输入、服务端 Schema 校验、结果恢复和取消；JSON 模式保留提交的原始文本，不经数值转换。
- 浅色/深色/跟随系统、中英文、桌面侧栏及移动端抽屉；最小适配宽度 360px。

## 配置

`host` 与 `app` 共享插件 ID，读取同一个 state scope。托管 Runtime 的文件位置为 `<INGOT_HOME>/runtimes/<runtime>/state/app.backend/config.toml`。缺失文件使用默认值；未知字段、读取或解码错误会使构造失败。文件直接使用 `[backend]`，不使用旧式 `[plugins."app.backend".backend]`：

```toml
[backend]
address = "127.0.0.1:7316"
replay_capacity = 1024
subscriber_buffer = 64
heartbeat_interval_seconds = 15
operation_retention = 128
max_asset_bytes = 67108864
```

| 字段 | 默认值 | 作用 |
| --- | --- | --- |
| `address` | `127.0.0.1:7316` | HTTP 监听地址，最终由 `net.Listen` 校验 |
| `replay_capacity` | 1024 | 进程内 SSE replay 记录容量 |
| `subscriber_buffer` | 64 | 每个 SSE subscriber 的事件缓冲 |
| `heartbeat_interval_seconds` | 15 | SSE 心跳秒数 |
| `operation_retention` | 128 | 保留的终态 Operation invocation 数量，运行中调用另行保留 |
| `max_asset_bytes` | 67,108,864（64 MiB） | 单次 Asset 上传上限 |

数值字段 `0` 选择默认值；负数无效，heartbeat 还检查 duration 溢出。`address` 空字符串选择默认值。`max_asset_bytes` 不是 JSON 请求上限，普通 JSON 请求另有固定的 1 MiB 上限。

Web 命令 `/app-webui config`（Group `app-webui`、Name `config`、输入 `{}`）通过结构化交互修改上述六项，并原子写入插件配置。配置交互期间文件发生变化时拒绝覆盖。返回六个规范化字段和 `restart_required`：保存后的有效配置与当前启动配置不同时为 `true`。**本插件不热更新监听地址、缓冲或其他服务器设置**；执行 `ingot restart web` 后才使用新配置。仅保存与当前有效值相同的配置时返回 `false`。结果反映已保存的目标配置，重启前 `/api/state` 仍反映当前实例的有效状态。

## HTTP 接口

当前后端提供以下接口，字段与投影定义见 [protocol.go](protocol.go)：

| 功能 | HTTP 接口 |
| --- | --- |
| 状态引导与事件 | `GET /api/state`、`GET /api/events` |
| Turn | `POST /api/turns`、`DELETE /api/turns/{id}` |
| Session | `GET/POST /api/sessions`、`GET/PATCH/DELETE /api/sessions/{id}`、`POST /api/sessions/{id}/workspace` |
| Session 生命周期 | `POST /api/sessions/{id}/archive`、`/restore`、`/fork` |
| Workspace 目录选择 | `POST /api/workspace/select` |
| 历史消息 | `GET /api/sessions/{id}/history` |
| Asset | `POST /api/assets`、`GET /api/assets/{id}` |
| Operation | `GET /api/operations`、`POST /api/operations/{internal-id}`、`DELETE /api/operation-invocations/{id}` |
| Interaction 响应 | `POST /api/interactions/{id}/response` |

常用请求体示例：

```json
{"title":"发布检查","workspace":"/absolute/path/to/project"}
```

上述请求用于 `POST /api/sessions`，返回 `201` 和会话投影（含 `id`）；省略/清空 workspace 使用默认工作区。`PATCH /api/sessions/{id}` 使用 `{"title":"新标题"}`；一次性绑定工作区使用 `{"workspace":"/absolute/path"}`。新建 Turn 使用：

```json
{"sessionId":"session-id","input":"检查工作区","attachments":[{"kind":"image","mimeType":"image/png","name":"example.png","assetId":"asset-id"}]}
```

附件可省略；Asset 必须先上传。Turn 被接受时返回 `202` 与 `{"id":"invocation-id"}`，取消使用该 invocation ID 而非 Session ID。响应 Interaction 使用 `{"values":{"answer":"回答文本"}}`，审批则使用 `{"values":{"decision":"allow"}}`；字段名以 pending request 声明为准，成功响应为 `204`。错误包装统一为 `{"error":{"code":"...","message":"..."}}`。

## Turn 与流式输出

存在流式能力时优先使用流式执行。`Stream` 返回错误后不会通过 `Run` 重试。运行中的 Turn 会在状态快照中暴露 `revision`、`output` 和 `reasoning`。输出和推理增量事件包含 `invocationId`、`revision` 与 `text`；客户端应忽略不大于当前投影 revision 的事件。

Turn 完成后会从运行中注册表移除，完整历史仍以 `agent.History` 为准。

`agent.invocation.started` 携带运行中 Turn 的快照。`agent.invocation.finished` 携带 `invocationId`、状态、执行结果统计，以及规范的 `result.output` 或错误详情。这些 Web 生命周期事件也能表示 SDK Turn 生命周期建立前发生的失败。

十种 `agent.turn/round/model/tool.*` 事件仅来自 Observation，并保留 SDK correlation、sequence 和物化时间。需要将 `host` 导出的 Observer 接入 Observation Consumer 才会收到这些事件；后端本身不会创建 Consumer，也不会合成执行事实。Web invocation ID 与 SDK turn ID 始终是两个独立标识。

历史消息和规范结果使用有序内容数组、字符串形式的 `kind`，以及显式的媒体来源。内联输出字节在 JSON 中编码为 base64；URI 和 Asset 输出来源会原样保留，不会被后端读取。Turn 输入仅接受基于 Asset 的附件。空文本和仅含附件的 Turn 会交由 Agent 的领域校验处理。未绑定 Workspace 的历史 Session 会在创建 Turn 前自动绑定默认工作区。

## Asset 上传与读取

Asset 上传直接使用请求体原始字节，并要求提供已知的 `Content-Length`。每个请求只上传一个 Asset。调用 Store 前会检查大小限制，零字节上传同样受支持。

上传成功返回 `201`：

```json
{
  "id": "asset-123",
  "size": 123
}
```

未提供长度时返回 `411`，超过大小限制时返回 `413`，未配置可选的 Asset Store 时返回 `501`。文件名和 MIME 元数据由后续创建 Turn 时的 Attachment DTO 提供。

`GET /api/state` 的 `assets` 字段返回 `available` 和 `maxBytes`。读取接口通过 `Store.Stat/Open` 流式传输已有 Asset；不存在时返回 `404`，未配置 Store 时返回 `501`。响应使用 `application/octet-stream`、`Content-Disposition: attachment`、`nosniff` 和 `no-store`，不会信任历史消息中的 MIME 类型来执行内容。

前端仅对允许的图片、音频和视频格式创建 Blob 预览；HTML、SVG 等文件保留为下载。Markdown 原始 HTML 被禁用，远程图片转换为显式链接，避免后台请求第三方资源。

## Operation

Operation 调用请求格式如下：

```json
{
  "sessionId": "optional-session-id",
  "input": {}
}
```

Operation Definition 按组件图提供的顺序生成快照，并在服务器开始监听前编译其 Draft 2020-12 Schema。Schema 必须自包含：支持本地 `$ref`，不会获取外部资源。输入和成功结果都必须是满足对应 Schema 的 JSON 对象。

每个 Definition 必须声明有效的局部 `Name`；`Group` 是可选的展示提示。Web UI 将有分组的定义投影为 `/<group> <name>` 两级 Slash Command，对空 Group 使用界面回退标签。空 Group 和重复 `(Group, Name)` 均允许，不会因此阻止启动；界面区分重复显示命令，HTTP 调用始终通过各自不同的 internal ID 路由。回退标签不会写回 Operation Group，命令文本不会发送给 Agent 或写入消息历史。

对话 Composer 输入 `/` 时先展示 Group，选中后再展示该 Group 的 Operation。完整命令会立即打开 Operation 弹窗，Interaction 表单、运行状态、结果和显式取消均在弹窗内完成；`//` 用于发送以 `/` 开头的普通消息。主导航不再暴露调试页，原 `/operations` 路由保留在“设置 → 开发者”中。

Operation 不存在时返回 `404`；输入无效时会在调度前返回 `400`。调用被接受后返回 `202` 和 invocation ID。`operation.started`、`operation.completed`、`operation.failed` 与 `operation.canceled` 事件均携带 invocation 快照，该快照也会出现在 `/api/state` 中。

Operation 状态包括 `running`、`succeeded`、`failed` 和 `canceled`。除全部运行中调用外，后端还会保留最近 `operation_retention` 个终态结果。输出未通过 Schema 校验时，invocation 会以 `operation_invalid_output` 错误结束，并且不会重试 Operation。

取消已经结束的调用返回 `409`；调用 ID 不存在或已被淘汰时返回 `404`。结果可在浏览器刷新后恢复，但进程重启或超过保留上限后无法恢复。

## SSE 与状态恢复

SSE 客户端必须先通过 `GET /api/state` 获取状态快照和 cursor，再连接：

```http
GET /api/events?after=<cursor>
```

请求的 cursor 已超出有界 replay 窗口时返回 `409`，客户端需要重新获取完整状态快照。

前端每次重连都重新引导，不自动重发 Turn、Operation 或 Interaction 提交。历史加载与 SSE 独立：Agent 正在执行时，History 可能等待该 Turn 收尾，但审批、流式输出和取消仍可使用。输出与推理共享 revision，重叠回放不会重复追加。

持久消息以 `agent.History` 为准；终态 Turn 用量、推理和 Observation 详情仅在当前连接的内存中可见，刷新/重连后不提供历史执行回放。Operation 结果按服务端保留策略恢复。未发送草稿和敏感输入不写入本地存储；只有语言、主题与面板偏好会保存。

## 生命周期

`app` 组件通过类型化的宿主依赖使用 ABI `invocation.Invocation` 和 `lifecycle.Controller`。`--ingot-check` 会校验依赖、配置和 Operation Schema，但不会监听配置的端口。HTTP 服务器异常退出时会请求关闭进程。

Cleanup 会取消 HTTP/SSE 请求和后台 invocation，等待 Turn 与 Operation 收尾；达到清理 deadline 时会强制关闭连接。单个 HTTP 请求或 SSE 连接关闭不会取消仍在运行的 Turn 或 pending interaction。

## Interaction

Interaction Request 会在注册前完成校验。提交的 JSON `null`、错误的基础类型、未知字段和声明选项之外的值都会被拒绝，且不会消费 pending request。

敏感默认值仅保留在服务端，settlement 事件不包含用户提交值。当前状态变更与对应事件保持一致顺序。Operation 使用的 Channel 会在 pending/state 快照和所有 Interaction 事件中携带 invocation scope。

普通 Channel 的 Request/Emit/Set/Clear 始终是全局作用域，不从 context 推导业务 routing。`tool.ask` 与审批通过 `interaction.ExecutionBinder.Bind(tool.Invocation.Scope)` 获得 execution-scoped Channel；显式 SessionID 是唯一 routing authority。SDK Observation correlation 只在 SessionID 与显式 Scope 一致时补充 Turn、Tool 和 Round 展示信息，缺失或冲突的 correlation 都不能移除或改写 Session routing。Operation 使用的私有 `Scoped` Channel 继续以显式 invocation scope 为准。

State ID 仍然等于 `State.Name`；scope 不会生成新的全局 State identity。

## 验证

在本模块目录中运行测试；以下环境变量写法适用于 POSIX Shell：

```sh
GOWORK=off go test -race ./...
```

PowerShell 使用 `$env:GOWORK = 'off'` 后执行 `go test ./...`；`-race` 需要当前平台具备相应 Go race/C 工具链。前端 lint、类型检查、单元测试与浏览器回归命令见 [前端开发说明](web/README.md)。现有后端测试覆盖真实 HTTP/SSE、工作区选择器、Asset、配置重启标记、Operation Schema、Interaction 作用域及进程关闭；浏览器 fixture 只在测试中提供。

返回 [插件文档索引](../docs/README.md)。

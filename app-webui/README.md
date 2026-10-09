# app.backend

`app.backend` 是 Ingot 的浏览器应用，包含 Vue 3 + Tailwind CSS 前端及 HTTP/SSE 应用边界。插件目录名为 `app-webui`，Go 模块为 `github.com/ingot-agent/plugins/app-webui`，manifest ID 为 `app.backend`，配置命令 Group 为 `app-webui`；这些标识各有用途，不能互换。[manifest](ingot.plugin.toml) 声明兼容 Ingot `>=0.3.0 <0.4.0`。它是一个包含两个组件的复合插件：

- `host`（包 `./host`）依赖 ABI `state.Scope`，持有进程内的 `EventHub`，导出 `appbackend.Runtime`、全局 `interaction.Channel`、显式作用域的 `interaction.ExecutionBinder` 和 `observation.Observer`。该组件不依赖 Agent，因此 Agent 可以使用这些能力而不会在组件图中形成环。
- `app`（包 `./app`）是没有能力导出的图叶节点，持有 HTTP 服务器、Controller、运行中的 Turn，以及保留的 Operation 结果。它依赖 host 的 `appbackend.Runtime`、`agent.History`、`session.Store`、`session.Manager`、`session.Query`、`workspace.Manager`、`workspace.Resolver`，以及 ABI `invocation.Invocation`、`lifecycle.Controller` 和 `state.Scope`。相互独立且可选的 `agent.Runtime` 与 `agent.StreamingRuntime` 至少需要提供一个。`asset.Store`、`modelselection.Controller` 和 `agent.PluginInputWriter` 是可选依赖；writer 用于在启动 Agent 前写入文件通知。Operation 通过 `[]operation.Operation` 收集；应用自身另外注册 `/app-webui config`。

模块要求 Go 1.24.2，已发布的直接 SDK/ABI 版本由 [go.mod](go.mod) 固定，当前分别为 SDK `v0.2.16`、ABI `v0.1.0`。本分支的本地文件输入通过 SDK 已发布的通用 writer 接口调用 [context-input](../context-input/README.md)，由它统一校验并使用 `session.Store.Append` 写入，读取时统一格式化；Runtime 需组合该新插件、支持插件输入投影的 Agent 和收集 Contributor 的 Prompt。独立模块验证使用 `GOWORK=off`，不需要本地 SDK 替换。插件没有主程序，应由 Ingot Builder 组合成 Runtime Image。

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
  选择器的 `initialPath` 使用已选择目录或 `workspace.defaultPath` 的绝对路径，
  界面上的默认工作区文案不作为路径传递。
- Linux Host 必须运行在设置了 `DISPLAY` 或 `WAYLAND_DISPLAY` 的桌面会话中，并安装
  `zenity` 或 `kdialog`。SSH 和无界面服务器不会打开选择器，接口返回
  `workspace_picker_unavailable`；默认工作区仍可直接使用。
- 会话搜索、新建、重命名、归档/恢复、分叉与确认删除；正在执行时禁用生命周期变更。
- 对话消息不显示 Ingot 图标/名称；实时执行仍展示状态徽标。工具调用显示开关保存在浏览器本地偏好中，刷新后也会应用到历史消息；隐藏纯工具消息时不影响最终回答。
- Markdown、代码高亮/复制、折叠推理、工具调用卡片和独立执行详情；Turn、Round、Model、Tool 与 Session 累计用量来自公开 SDK 能力。
- 流式输出及 Run-only 降级、停止执行、内联审批/自由输入、跨会话待处理请求抽屉。
- 通过宿主的系统文件选择器多选本地文件。图片按 Asset 能力预览并作为模型图片输入；其他文件以原路径通知模型，不复制文件。文件选择不依赖 Asset 能力。
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
| `max_asset_bytes` | 67,108,864（64 MiB） | 单个所选文件及单次 Asset 上传上限 |

数值字段 `0` 选择默认值；负数无效，heartbeat 还检查 duration 溢出。`address` 空字符串选择默认值。`max_asset_bytes` 不是 JSON 请求上限，普通 JSON 请求另有固定的 1 MiB 上限。

Web 命令 `/app-webui config`（Group `app-webui`、Name `config`、输入 `{}`）通过结构化交互修改上述六项，并原子写入插件配置。配置交互期间文件发生变化时拒绝覆盖。返回六个规范化字段和 `restart_required`：保存后的有效配置与当前启动配置不同时为 `true`。**本插件不热更新监听地址、缓冲或其他服务器设置**；执行 `ingot restart web` 后才使用新配置。仅保存与当前有效值相同的配置时返回 `false`。结果反映已保存的目标配置，重启前 `/api/state` 仍反映当前实例的有效状态。

## HTTP 接口

当前后端提供以下接口，字段与投影定义见 [protocol.go](protocol.go)：

| 功能 | HTTP 接口 |
| --- | --- |
| 状态引导与事件 | `GET /api/state`、`GET /api/events` |
| 模型选择 | `GET/PUT /api/model-selection` |
| Turn | `POST /api/turns`、`DELETE /api/turns/{id}` |
| Session | `GET/POST /api/sessions`、`GET/PATCH/DELETE /api/sessions/{id}`、`POST /api/sessions/{id}/workspace` |
| Session 生命周期 | `POST /api/sessions/{id}/archive`、`/restore`、`/fork` |
| 原文追问 | `GET/POST /api/sessions/{id}/followups`、`DELETE /api/followups/{id}` |
| Workspace 目录选择 | `POST /api/workspace/select` |
| 本地文件选择 | `POST /api/files/select` |
| 历史消息 | `GET /api/sessions/{id}/history` |
| Asset | `POST /api/assets`、`GET /api/assets/{id}` |
| Operation | `GET /api/operations`、`POST /api/operations/{internal-id}`、`DELETE /api/operation-invocations/{id}` |
| Interaction 响应 | `POST /api/interactions/{id}/response` |

`modelselection.Controller` 定义在本插件的 [modelselection 包](modelselection/selection.go)，由其他插件实现并通过组件图注入。它只提供当前有效选择、实时 provider/model/强度目录及带修订号的更新；WebUI 不读取实现插件的配置。未注入时接口返回 `501`，界面隐藏切换控件。模型目录为空的 provider 不可在界面中选择模型。

当前官方实现由 [model-runtime](../model-runtime/README.md) 提供，选择器修改它的
默认供应商、模型和推理强度，与 `/model-runtime config` 使用同一份配置和冲突检查。
Agent 不再保存独立的模型覆盖配置。契约保留在 WebUI 的公开包中，用于展示插件可自行
发布能力 SDK，并由其他插件实现，无需把新能力加入官方 SDK。

`PUT /api/model-selection` 使用 `{"revision":"...","selection":{"provider":"...","model":"...","reasoningEffort":"low"}}`。`reasoningEffort` 为 `providerDefault` 表示明确使用供应商默认值；提交时实现者必须重新校验实时目录并保存。旧修订号返回 `409`，无效选择返回 `400`。成功后发布 `model.selection.updated` 事件，并只影响之后开始的 Turn。

常用请求体示例：

```json
{"title":"发布检查","workspace":"/absolute/path/to/project"}
```

上述请求用于 `POST /api/sessions`，返回 `201` 和会话投影（含 `id`）；省略/清空 workspace 使用默认工作区。`PATCH /api/sessions/{id}` 使用 `{"title":"新标题"}`；一次性绑定工作区使用 `{"workspace":"/absolute/path"}`。新建 Turn 使用：

```json
{"sessionId":"session-id","input":"检查报告","attachments":[{"kind":"file","path":"/absolute/path/to/report.pdf"}]}
```

附件可省略；前端提交选择接口返回的 Attachment DTO，后端在发送时重新校验原路径并生成文件元数据。Turn 被接受时返回 `202` 与 `{"id":"invocation-id"}`；前端直接使用本次输入与所选文件列表显示临时消息，持久历史仍以 `agent.History` 为准。插件上下文消息保留在历史数据中，但不渲染为聊天气泡。外壳统一为 `<system source="plugin">...</system>`，不包含插件名；历史文件展示通过有序 JSON 文件列表和固定通知正文识别。取消使用该 invocation ID 而非 Session ID。响应 Interaction 使用 `{"values":{"answer":"回答文本"}}`，审批则使用 `{"values":{"decision":"allow"}}`；字段名以 pending request 声明为准，成功响应为 `204`。错误包装统一为 `{"error":{"code":"...","message":"..."}}`。

原文追问在点击追问时 fork 当前 Session 并保存便签，即使尚未发送问题也可收起后重开；后续 Turn 使用返回的便签 Session ID。创建请求包含 `messageIndex`、`partIndex`、`start`、`end` 和 `quote`；返回值还包含 `baseMessageCount`，供界面隐藏 fork 时复制的主对话历史。已保存的选区以橙色标注，直接点击原文即可重开便签；多个便签可同时作为可移动、可缩放的小窗显示，点击窗口会将它置顶。WebUI 将锚点、主会话 ID 和历史边界写入追问 Session 的 `Meta["app-webui"]`，不写入模型上下文，也不使用单独的 `inline-followups.json`；普通 Session 列表不会显示便签。删除主 Session 时会先删除其便签。旧 JSON 不迁移，旧追问 Session 可能作为普通会话显示。

## Turn 与流式输出

存在流式能力时优先使用流式执行。`Stream` 返回错误后不会通过 `Run` 重试。运行中的 Turn 会在状态快照中暴露 `revision`、`output` 和 `reasoning`。输出和推理增量事件包含 `invocationId`、`revision` 与 `text`；客户端应忽略不大于当前投影 revision 的事件。

Turn 完成后会从运行中注册表移除，完整历史仍以 `agent.History` 为准。

`agent.invocation.started` 携带运行中 Turn 的快照。`agent.invocation.finished` 携带 `invocationId`、状态、耗时和失败信息，以及规范的 `result.output` 或错误详情。这些 Web 生命周期事件也能表示 SDK Turn 生命周期建立前发生的失败。

十种 `agent.turn/round/model/tool.*` 事件仅来自 Observation，并保留 SDK correlation、sequence 和物化时间。需要将 `host` 导出的 Observer 接入 Observation Consumer 才会收到这些事件；后端本身不会创建 Consumer，也不会合成执行事实。Web invocation ID 与 SDK turn ID 始终是两个独立标识。

历史消息和规范结果使用有序内容数组、字符串形式的 `kind`，以及显式的媒体来源。内联输出字节在 JSON 中编码为 base64；URI 和 Asset 输出来源会原样保留，不会被后端读取。Turn 输入接受选择器返回的本地路径；旧式仅含 Asset 的输入只接受 PNG、JPEG、GIF、WebP 图片，其他格式返回 `invalid_local_file`。仅选择文件而不输入文字时，先写入文件通知，再保留一个空用户消息。未绑定 Workspace 的历史 Session 会在创建 Turn 前自动绑定默认工作区。

## Session Token 用量

会话查询和 `/api/state` 的 Session 投影包含 `totalToken`，来自 Session 持久化的累计值。右侧栏的 Token 卡片只显示当前会话累计 Token 和当前上下文总量，展开后的子任务工具详情显示子会话累计 Token；追问便签标题不显示用量。Turn 结果仅保留状态、耗时和失败信息，不再采集执行次数统计。

Token 卡片同时显示 `context-compact.session-context/<sessionId>` 的 `inputTokens`，
表示压缩插件对最近一次实际请求输入的估算，包含系统提示词、工具和压缩后的历史，
不包含尚未计入下一次请求的新输出，允许压缩后数值下降。
当前 Runtime 的状态快照可在页面刷新后恢复该值；Runtime 重启后，未计数的会话显示不可用。

右侧栏不渲染 Turn、Round 执行详情或逐 Turn 用量，也不显示模型标识、估算精度和最近请求输入说明。

模型供应商成功报告用量后，由 ModelRuntime 先持久化，再通过绑定 Session 的 `interaction.Channel.Set` 发布 `model-runtime.session-usage/<sessionId>`。Values 为 `sessionId` 和 `totalToken`；每个 Session 使用独立状态名，前端按累计快照的最大值归并，重复事件或旧查询不会重复加数。刷新与重开会从 Session 查询恢复，不依赖 Turn 或 Observation 回放。

普通会话与普通 Fork 的 root/current 均为自身，Fork 初值为 0。追问的 root 由服务端读取其 `Meta["app-webui"].sourceSessionId` 并校验，current 为便签自身；浏览器只提交 current。根会话累计包含所属子任务、压缩和追问，不能把全部 Session 的累计值相加作为全局消耗。删除成功后 Clear 对应状态，前端忽略已删除 Session 的晚到快照。

此合同使用 SDK v0.2.15 发布的 Runtime 双 Session 参数及 `session.Metadata.TotalToken`，应组合相容版本的 Agent、ModelRuntime、Compactor 和 Session 插件。本分支新增的文件输入使用 SDK v0.2.16 已发布的通用接口；使用 `go.mod` 固定的依赖进行 `GOWORK=off` 独立验证，无需本地 SDK workspace。

## 本地文件输入（未发布）

`POST /api/files/select` 接受 `{"initialPath":"/absolute/path/to/directory"}`，省略或留空时使用默认工作区。前端默认传当前会话的工作区。目录必须存在；文件与目录选择器共用一个占用锁，同时选择返回 `workspace_picker_busy`。取消返回 `{"files":[]}`。成功返回：

```json
{
  "files": [
    {
      "kind": "file",
      "mimeType": "application/pdf",
      "name": "report.pdf",
      "path": "/absolute/path/to/report.pdf",
      "size": 123
    }
  ]
}
```

路径由后端解析符号链接并规范为绝对路径，必须是现存的普通文件，大小不超过 `max_asset_bytes`。文件名、MIME 和大小由后端读取，不信任浏览器提交的同名字段。PNG、JPEG、GIF、WebP 在配置 Asset Store 时导入现有 Asset 机制并返回 `assetId`，用于图片预览及模型图片输入；没有 Asset Store 时只提供路径。其他格式（包括文档、音视频、SVG、AVIF）不读取正文或复制到插件目录，也不发送为供应商媒体输入。

发送时，WebUI 为本次选择的所有文件生成一条 `app.backend` 插件输入，正文包含 JSON 文件列表及“以上是紧随其后的用户输入所上传的文件。”。完成文件、会话和工作目录校验后，HTTP app 调用 `agent.PluginInputWriter.Append`，由 `context.input` 校验、编码并通过普通 `session.Store.Append` 写入。通知写入成功后才启动 Agent，由 Agent 正常读取历史、保存用户输入并生成首次模型请求；后续轮次沿用这个上下文。通知写入失败时直接返回错误，不启动 Turn，也不自动重试。

校验、Entry 格式、XML 外壳及来源系统说明均由 `context.input` 提供。WebUI 仅负责文件正文与追加时机，不依赖该插件的实现包。没有 writer 时不向界面声明文件选择可用，提交本地文件返回 `501`，不启动 Turn。通知文本校验在 `Append` 时进行；被拒绝时不启动 Agent，本次用户消息尚未保存。

WebUI 正常流程中文件通知位于对应用户消息之前，两者是独立的 Append，不提供成对原子提交、失败后的自动重试或其他写入者之间的隔离。通知成功后、用户消息保存前中断，可能只留下通知记录；已提交的通知保留在历史中。这个顺序由 WebUI 的调用约束实现，SDK 没有额外的文件关联接口或历史分组格式。

发送后的临时消息直接使用文件选择器返回的文件列表；读取历史后，界面从持久通知中提取文件信息。两者均按选择顺序显示在所属用户消息中；纯文件发送显示附件，非图片暂仅显示图标、文件名和类型。上下文保留空用户消息，插件通知在主对话和追问窗口中均不渲染，发送中和刷新后遵循同一显示规则。原始历史和消息索引保持完整，重启及 Fork 保留上下文顺序。

选择器打开在 Runtime 所在主机：Windows 使用 COM `IFileOpenDialog`，macOS 使用 `NSOpenPanel`，Linux 使用 Zenity 或 KDialog，Linux 仍需要桌面会话。不可用时返回 `file_picker_unavailable`。浏览器原生文件输入不暴露真实绝对路径，因此这里调用宿主选择器；远程浏览器不能用此接口选择远程设备上的文件。当前只支持文件选择，不处理粘贴或拖放文件；原文件后续被移动、删除或修改时，路径通知不会保存一份副本。

此前已经写入历史的非图片媒体附件不会自动改写为路径通知，继续使用这些旧记录仍可能被供应商拒绝；可在新会话重新选择原文件。此实现不迁移旧历史。

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

`GET /api/state` 的 `assets` 字段返回 `available` 和 `maxBytes`；`files` 字段独立返回本地文件选择能力和同一文件大小上限。读取接口通过 `Store.Stat/Open` 流式传输已有 Asset；不存在时返回 `404`，未配置 Store 时返回 `501`。响应使用 `application/octet-stream`、`Content-Disposition: attachment`、`nosniff` 和 `no-store`，不会信任历史消息中的 MIME 类型来执行内容。

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

命令面板支持上下方向键循环切换选项；选中项超出列表可视范围时，列表会自动滚动以显示该项，输入焦点仍保留在 Composer。

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

持久消息以 `agent.History` 为准；Session 累计 Token 从 Session 查询恢复。终态 Turn 执行指标、推理和 Observation 详情仅在当前连接的内存中可见，刷新/重连后不提供历史执行回放。Operation 结果按服务端保留策略恢复。未发送草稿和敏感输入不写入本地存储；只有语言、主题与面板偏好会保存。

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
GOWORK=off go mod tidy -diff
GOWORK=off go vet ./...
GOWORK=off go test -race ./...
```

PowerShell 使用 `$env:GOWORK = 'off'` 后执行上述 Go 命令；`-race` 需要当前平台具备相应 Go race/C 工具链。前端 lint、类型检查、单元测试与浏览器回归命令见 [前端开发说明](web/README.md)。后端单元测试使用 SDK 接口替身，跨插件集成可通过 workspace 组合 Agent、Prompt、context.input 和本模块进行验证；浏览器 fixture 只在测试中提供。

返回 [插件文档索引](../docs/README.md)。

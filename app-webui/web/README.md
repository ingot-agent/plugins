# Ingot Web UI

Vue 3 / TypeScript / Vite / Tailwind CSS 4 单页工作区。使用 Pinia、Vue Router hash 路由、Vue I18n、Reka UI、Lucide、markdown-it 和 highlight.js。后端及生产启动方式见 [app.backend](../README.md)。

## 开发与构建

`package.json` 要求 Node `>=22.12`，推荐 Node 24；需要 npm 与 Go（浏览器 fixture 会运行 Go 测试）。在本目录执行：

```sh
npm ci
npm run dev
```

Vite 监听 `127.0.0.1:5173`，将 `/api` 代理到 `http://127.0.0.1:7316`。先启动已组合的 Web 后端，或使用下方测试服务。其他后端地址使用 `INGOT_API_URL=http://127.0.0.1:PORT npm run dev`。

上述行内环境变量写法适用于 POSIX Shell。PowerShell 中改为：

```powershell
$env:INGOT_API_URL = 'http://127.0.0.1:7316'
npm run dev
```

```sh
npm run lint
npm run typecheck
npm test
npm run build
```

`build` 先做 Vue/TypeScript 类型检查，再清空并重新写入 `../app/webdist/`。源代码、lockfile 和这个嵌入目录一起提交；Go 用户无需安装 Node。`npm run check:dist` 重建并检查该目录相对于 Git 的已暂存、未暂存及未跟踪差异，供干净 checkout 的 CI 检查产物是否过期；本地尚未提交产物时出现差异是预期的。只修改前端源文件不会改变已经嵌入现有 Runtime Image 的 UI，需重新构建该本地模块对应的 Runtime Image 并重启。

`npm run preview` 用于本地预览已构建产物。Vite 本身不提供 Agent 能力，涉及会话、执行和配置时仍需要 Go 后端；生产 UI 由 Go 服务同源提供。

## 浏览器回归

```sh
npx playwright install chromium
npm run build
npm run test:e2e
```

Playwright 启动两个真实 Go HTTP/SSE 服务（`17316` 流式、`17317` Run-only），使用仅存在于 Go 测试代码中的确定性 SDK adapters，不访问模型、不需要凭据。覆盖会话生命周期、审批与自由文本、跨标签页同步、运行中历史阻塞、取消后的部分输出、附件历史、Operation、过期 cursor 和中英文/深色/移动布局。失败时保留 trace、截图及 HTML 报告。

默认 fixture 使用 `GOWORK=off` 验证插件声明的已发布依赖。跨仓库 contract
尚未发布时，可临时指定本地 workspace，例如
`INGOT_WEBUI_FIXTURE_GOWORK=/absolute/path/to/go.work npm run test:e2e`；该选项只影响
测试子进程，不改变插件的发布依赖。

PowerShell 对应设置为 `$env:INGOT_WEBUI_FIXTURE_GOWORK = 'D:\absolute\path\go.work'`；测试结束后可用 `Remove-Item Env:INGOT_WEBUI_FIXTURE_GOWORK` 恢复默认。不要将本机路径提交进模块或 package.json。

使用已有 Chromium 时可指定 `INGOT_TEST_CHROMIUM=/absolute/path/to/chrome npm run test:e2e`。手动预览测试数据：

```sh
INGOT_WEBUI_FIXTURE_ADDR=127.0.0.1:7316 node scripts/fixture.mjs
```

打开 `http://127.0.0.1:7316/`；输入含 `approve`、`ask`、`hold`、`fail` 分别触发审批、自由输入、等待取消和失败。其他输入返回固定文本。这不是生产 Agent，数据只存在于进程内；重新构建嵌入文件后需要重启测试服务。

fixture 通过 `go test -run '^TestBrowserFixture$' -count=1 -timeout 0 -v ./app` 启动；Playwright 自己负责启动两个 fixture，不会复用端口上已有的服务。若端口被占用、Go 依赖不可获得或 Chromium 未安装，先解决对应环境问题；修改 Web 界面后必须先执行 `npm run build`，浏览器测试读取的是 Go 嵌入产物而不是 Vite 开发服务器。

## 状态边界

Operation 的 Interaction 表单按层级编辑 Object/List：先显示字段或列表摘要，点击后在同宽区域进入下一层，使用返回按钮或面包屑返回。未提交的值在层级切换时保留，校验失败会定位到对应字段。Operation 弹窗和调试页使用单行输入；普通对话中的自由文本回复仍支持多行。弹窗的提交按钮固定在底部。

- `api.ts` / `sse.ts`：JSON 命令与 fetch SSE，错误不会触发自动重试执行。
- `state.ts` / `stores/runtime.ts`：快照 + cursor 引导、revision 去重、请求代际保护、权威历史替换，以及有限的当前连接执行记录。实时 Turn 按收到事件的顺序追加正文、独立推理段、工具调用和 Interaction；连续文本增量合并，工具进度与结果更新原卡片。新模型调用会开始新的文本段。
- `forms.ts`：Interaction 字段转换与 Operation 简单 Schema 判断；复杂结构或不安全的大整数保留在 JSON 模式提交，完整校验交给后端。
- `components/` / `views/`：会话、执行详情、审批、附件和 Operation；用户文本不作为原始 HTML 执行。

Web invocation ID 与 SDK Turn ID 不可互换；仅在会话中能明确关联执行时，将 Observation 放进对应的实时 Turn。Observation 是只读事实，不用它推断 Web 请求是否已成功；终态以 Web invocation 事件、规范结果和 History 协调。执行结束后，正文和工具调用由权威历史替换。刷新/重连不恢复过去的推理分段、用量或详细 trace；运行中快照只能展示已有的汇总文本，后续事件继续向下追加，也不做 Checkpoint/Resume。

# 可复现的官方插件组合

这里提供三个完整的 `plugins.toml`，分别覆盖基础聊天、文件编辑与审批、子代理。
这些是**当前源码的本地 recipe**，每项都有完整 module path 和相对于 recipe
文件的本地路径，不依赖 `ingot init` 隐式加入模块，也不依赖用户已有配置。
通用 recipe/lock 格式由
[Core 文件参考](https://github.com/ingot-agent/ingot/blob/main/docs/FILE_FORMATS.md)
维护；本页负责具体插件组合、配置及使用边界。

Core 的发布 profile 和本仓库 Collection 仍固定 `v0.1.0`。本页当前源码包含
之后的能力变化，`tool-subagent` 也不能假定已有相同版本的公开 tag。不要把
下面所有 `path` 简单换成 `version = "v0.1.0"` 后视为等价组合。

## 选择与完整顺序

| 组合 | 完整 recipe | 数量 | 能力 |
|---|---|---:|---|
| 基础聊天 | [base-chat.toml](recipes/base-chat.toml) | 9 | 浏览器、会话持久化、Chat Completions 模型；无模型工具 |
| 文件编辑与审批 | [edit-approval.toml](recipes/edit-approval.toml) | 11 | 基础聊天 + 读取/搜索/修改已有文件 + 工具审批 |
| 子代理 | [child-agents.toml](recipes/child-agents.toml) | 12 | 文件编辑与审批 + 子 Agent 类型发现、派发、查询、取消及结果提交 |

表中“无模型工具”不妨碍基础组合的配置 Operations；它们是另一类能力。
以下数字是 recipe 顺序。依赖关系仍决定实际构造先后；集合注入按已解析的
确定性顺序工作，不能把改动 recipe 顺序视为无意义的格式调整。

| 顺序 | 基础聊天 | 文件编辑与审批 | 子代理 |
|---:|---|---|---|
| 1 | asset-local | asset-local | asset-local |
| 2 | http-default | http-default | http-default |
| 3 | model-openai-compatible | model-openai-compatible | model-openai-compatible |
| 4 | model-runtime | model-runtime | model-runtime |
| 5 | tool-runtime | tool-edit | tool-edit |
| 6 | prompt-default | interceptor-approval | tool-subagent |
| 7 | session-sqlite | tool-runtime | interceptor-approval |
| 8 | agent-default | prompt-default | tool-runtime |
| 9 | app-webui | session-sqlite | prompt-default |
| 10 | — | agent-default | session-sqlite |
| 11 | — | app-webui | agent-default |
| 12 | — | — | app-webui |

`asset-local` 同时满足模型与 Agent 的 Asset 依赖；`session-sqlite` 提供
Session 和 Workspace 能力；`app-webui` 的 host 组件提供执行作用域
Interaction，再由 app 组件消费 Agent。子代理依赖同一个 `agent-default`
插件的 session-tree 组件，不需要第二个 Agent Runtime，也不会形成
`agent → tool runtime → child tools → agent` 的实现循环。

## 独立环境准备

目录布局如下，`recipe-lab/` 是新建的测试目录，不能使用正在运行的生产 Home：

```text
work/
  ingot-agent/             # Core 源码，可使用已安装的当前 CLI
  plugins/                 # 本仓库
  sdk/
  ingot-abi/
  recipe-lab/
    go.work
    .ingot/                # 本页专用 Managed Home
```

需要 Go 1.24.2+ 和当前 Core CLI。完整构建第一次需要联网下载 Go 依赖，
模型 API Key 不参与解析和构建。先在 `recipe-lab/go.work` 保存：

```go
go 1.24.2

use ../ingot-agent

replace github.com/ingot-agent/sdk => ../sdk
replace github.com/ingot-agent/ingot-abi => ../ingot-abi
```

以下命令都从 `recipe-lab/` 执行。Core 的解析逻辑会从当前目录向上查找
第一个 `go.work` 的 replacement，把 SDK/ABI 本地来源写入 lock；Go 子进程
本身仍使用 `GOWORK=off`。这是显式的开发组合，不是已发布依赖验证。
不要把 replacement 加进各插件的 `go.mod`。

当前 Builder 在全新缓存上可能在 build 的离线 `go mod download all` 阶段
报 `module lookup disabled by GOPROXY=off`。本次遇到的具体缺项是
`github.com/chzyer/readline@v1.5.1` 所需的旧版
`golang.org/x/sys@v0.0.0-20220310020820-b874c991c1a5`。这是解析/离线缓存
完整性问题，不能靠更新插件业务配置解决。本次验证先在同一个专用 Home
缓存预取该确切依赖，再重试 build；没有改动插件源码或 `go.mod`。

若日志显示相同缺项，可从 `recipe-lab/` 执行以下临时处理（POSIX shell）：

```sh
GOWORK=off GOMODCACHE="$(pwd)/.ingot/cache/gomod" go mod download golang.org/x/sys@v0.0.0-20220310020820-b874c991c1a5
```

PowerShell 等价写法会恢复原环境变量：

```powershell
$savedWork = $env:GOWORK
$savedCache = $env:GOMODCACHE
try {
    $env:GOWORK = 'off'
    $env:GOMODCACHE = [IO.Path]::GetFullPath('./.ingot/cache/gomod')
    go mod download golang.org/x/sys@v0.0.0-20220310020820-b874c991c1a5
    if ($LASTEXITCODE -ne 0) { throw 'Dependency prefetch failed' }
} finally {
    $env:GOWORK = $savedWork
    $env:GOMODCACHE = $savedCache
}
```

这项预取需要网络，只填充指定的测试缓存；它不是发布验收的永久替代。
发布前仍应修复并验证全新缓存下的完整流程。其他缺项应按实际日志排查，
不要删除 lock 或取消 `--locked` 来掩盖依赖问题。

```sh
ingot --home ./.ingot setup
ingot --home ./.ingot project resolve -f ../plugins/docs/recipes/base-chat.toml
ingot --home ./.ingot project show -f ../plugins/docs/recipes/base-chat.toml
ingot --home ./.ingot build chat -f ../plugins/docs/recipes/base-chat.toml --locked
ingot --home ./.ingot runtime command set chat -- web
ingot --home ./.ingot start chat
```

成功后打开 `http://127.0.0.1:7316/`。前台终端保持运行，`Ctrl+C` 停止；
后续也可以执行 `ingot --home ./.ingot start chat -d` 后台启动，并用
`ingot --home ./.ingot stop chat` 停止、`ingot --home ./.ingot logs chat` 看日志。

Recipe 旁会产生对应的 lock 文件及写入锁文件。这些是本地验证产物，包含
机器路径与本地内容摘要，不应提交成面向所有人的发布锁。可以先将 recipe
复制到项目目录再改写全部 `path`；相对路径必须相对于**新 recipe 所在目录**。
修改源码或配置组合后重新 resolve/build；运行中的实例需要重启或 `up` 才使用新 Image。

三个 Runtime 默认都监听 7316。逐个停止后再启动另一个，或者通过
`/app-webui config` 给它们设置不同的回环地址并重启。浏览器当前面向本机可信
单用户，不应直接公开这个具有配置、执行与审批权限的 HTTP 接口。

## 基础聊天：首次配置和验收

在全新的 `chat` Runtime 中：

1. 用 `/model-openai-compatible config` 新增名为 `primary` 的 provider，
   填入真实 API base URL 和凭据。例如兼容服务器需要 `/v1` 时，应填
   `https://你的服务/v1`，不要填到 `/chat/completions`。名称、URL 和模型
   只是配置，教程没有假设某个第三方服务或模型已经可用。
2. 用 `/model-runtime config` 选中 `primary`，填写该服务实际支持的模型 ID。
   适配器的模型列表是 allowlist，不会向服务请求模型目录。
3. 保留 `/agent-default config` 的 provider/model 为继承模式；新 Runtime
   没有旧覆盖。若复用 Runtime，先检查旧 override 是否改变了选择。
4. 新建 Session，发送一句普通文本，确认得到模型答复，并在刷新后仍可读取
   Session 历史。这个组合没有 shell、文件编辑或提问工具。

模型配置保存于该 Runtime 的
`state/model.openai-compatible/config.toml` 和 `state/model.runtime/config.toml`。
配置 Operation 使用 `{}` 输入并通过表单收集数据，不能直接把 TOML 传给 Operation。
真实凭据不写入 recipe、lock 或版本控制。完整字段见
[适配器](../model-openai-compatible/README.md)、[模型路由](../model-runtime/README.md)
和[配置指南](configuration.md)。

## 文件编辑与审批：配置和验收

先停止 `chat`，构建另一个 Runtime：

```sh
ingot --home ./.ingot project resolve -f ../plugins/docs/recipes/edit-approval.toml
ingot --home ./.ingot build editing -f ../plugins/docs/recipes/edit-approval.toml --locked
ingot --home ./.ingot runtime command set editing -- web
ingot --home ./.ingot start editing --foreground
```

每个 Runtime 有独立状态，按上一节重新配置模型。通过
`/interceptor-approval config` 保存以下策略；也可在 Runtime **停止时**把
下面内容保存为 `.ingot/runtimes/editing/state/interceptor.approval/config.toml`：

```toml
default_action = "ask"
argument_display = "full"
max_display_bytes = 4096

[[rules]]
tool = "read_file"
action = "allow"

[[rules]]
tool = "search"
action = "allow"

[[rules]]
tool = "edit_file"
action = "ask"
```

`allow` 是本 recipe 的显式选择；没有配置时插件对所有工具询问。
直接写文件后需要 start/restart，Operation 更新则立即作用于后续调用。
策略按工具精确名称匹配，不按路径或参数匹配；审批也不是系统沙箱。

在一个专用的真实目录手工创建 UTF-8 文件 `hello.txt`，内容为 `before`。
新建 Session 时选择此目录，随后让模型读取文件并将 `before` 换成 `after`。
应观察到以下步骤：

1. `read_file` 可直接读取；`edit_file` 提出一次审批，展示 path/old/new 参数。
2. 第一次选择拒绝，确认磁盘仍为 `before`。拒绝会使本次工具调用返回错误，
   当前 Turn 可能失败；这是审批拒绝的预期语义。
3. 新发一次明确编辑请求，允许后确认磁盘为 `after`，工具显示编辑摘要。
4. 尝试让模型读取工作区以外的绝对路径，工具应给出路径策略拒绝。

如果模型没有发起指定工具调用，不能把一段普通模型回答当成工具验收。
工具本身的确定性行为在 [tool-edit 测试](../tool-edit/tooledit_test.go)及
[审批测试](../interceptor-approval/approval_test.go)覆盖。

`edit_file` 只能修改已有文件，不能创建文件；本组合没有 shell。
读写默认单文件阈值 1 MiB，搜索累计扫描预算 4 MiB。路径规范检查不隔离
symlink 或其他进程，并发编辑没有事务式冲突检测。Session Workspace
绑定建立后不可更改，需要新的工作区时创建新 Session。详见
[tool-edit](../tool-edit/README.md)和[interceptor-approval](../interceptor-approval/README.md)。

## 子代理：配置和验收

这个组合在文件编辑与审批基础上增加 `tool-subagent`。构建并绑定后，先不启动：

```sh
ingot --home ./.ingot project resolve -f ../plugins/docs/recipes/child-agents.toml
ingot --home ./.ingot build children -f ../plugins/docs/recipes/child-agents.toml --locked
ingot --home ./.ingot runtime command set children -- web
```

创建 `.ingot/runtimes/children/state/agent.default/` 目录，并保存
`subagents.toml`（本文件与 `config.toml` 并列）：

```toml
subagents_config_version = 1
root_allowed_types = ["reviewer"]

[[agents]]
name = "reviewer"
description = "Read project files and report one bounded review."
system_prompt = "Read only what the assigned review needs. Finish by calling submit_agent_result alone with a concise report."
tools = ["read_file", "search", "submit_agent_result"]
allowed_child_types = []
```

这是具体类型的工具白名单：reviewer 不能编辑文件，也不能再派发子代理。
该文件在此 recipe 中演示显式覆盖；若省略，当前源码的 `agent.default` 会提供
内建 `coder`、`explorer`、`reviewer`，并只允许各自白名单中已安装的工具。
不要遗漏 `submit_agent_result`；普通文本回复不会替代正式结果提交。
宿主提供 `agent.Children` 不代表已启用类型：本组合若没有显式文件，会加载
内建类型；若显式文件为空，则查询/派发返回 unsupported。配置文件只在
session-tree 构造时读取，不由 `/agent-default config` 修改，改后需要重启。

创建 `.ingot/runtimes/children/state/interceptor.approval/`，保存
`config.toml`：

```toml
default_action = "ask"
argument_display = "full"
max_display_bytes = 4096

[[rules]]
tool = "read_file"
action = "allow"

[[rules]]
tool = "search"
action = "allow"

[[rules]]
tool = "list_agent_types"
action = "allow"

[[rules]]
tool = "check_agent"
action = "allow"

[[rules]]
tool = "wait_agent"
action = "allow"

[[rules]]
tool = "list_agents"
action = "allow"

[[rules]]
tool = "submit_agent_result"
action = "allow"
```

派发、取消和根 Agent 的编辑仍按默认 `ask` 询问；这份策略没有授予 child
额外工具权限，工具白名单由冻结的 child definition 决定。
现在执行 `ingot --home ./.ingot start children --foreground`，配置模型，
新建绑定测试目录的根 Session。

建议验收请求：“先查询允许的 Agent 类型，然后派发 reviewer，读取
hello.txt 并报告内容；不要修改文件，等待并给出其提交的结果。”
在工具卡片确认 `list_agent_types` 包含 `reviewer`，`spawn_agent` 使用
**存在的绝对目录**作为 `workspace_root`，批准派发后查看完成状态与结果。
若模型选择异步派发，使用 `check_agent`/`wait_agent` 查询其返回的 Session ID。

```json
{"agent_type":"reviewer","task":"Read hello.txt and report its contents without changing files.","workspace_root":"/absolute/path/to/test-directory","wait":false}
```

上面的 JSON 是 `spawn_agent` 工具参数形状；将路径替换为本机目录，Windows
JSON 可使用 `D:/path/to/test-directory`。它不是可直接发送到浏览器的独立工具
HTTP API，也不是启动 CLI 的参数。

子 Agent 当前是单 Turn 任务。定义、任务与工作区关系被保存；进程重启会把
排队/运行中的任务标为 interrupted，不会自动续跑。`wait_agent` 超时不取消
任务；需要停止时调用 `cancel_agent`。当前固定限制包括深度 8、排队 64、
活动任务（含排队）128、任务 deadline 30 分钟，这些不是可写的 TOML 参数。
多个 child 可以共享目录，调度器不创建 Git worktree，也不隔离并发文件修改。
更完整状态/错误语义见 [agent-default](../agent-default/README.md#child-agent-configuration)
和 [tool-subagent](../tool-subagent/README.md)。

## 组合扩展与发布验证

新增插件前先检查其强制/可选依赖及单值 provider 歧义。尤其注意：

- 加入 `tool-shell` 会授予模型运行宿主 shell 的能力；审批只经过
  `tool.Runtime` 拦截，Workspace 和本机用户权限不是系统沙箱。
- `model-openai-responses` 与 Chat Completions 适配器可提供不同名称的
  provider，但名称必须唯一；本页没有验证 Responses。
- 当前 `context-compact` 的摘要请求包含非 nil 空 Stop，Responses 适配器
  拒绝该参数，不能把二者的组合当作已验证配方。
- 本页没有安装 `skill-runtime`、用量统计或脚本拦截器。需要时读对应模块
  README，补齐配置、构建及业务调用验证，不能只检查 manifest 存在。

发布版 recipe 应使用实际存在的各模块精确版本，SDK 则由插件 `go.mod` 的
真实依赖选择。移到没有祖先 `go.work` replacement 的独立目录，先核对模块
tag，再 resolve、build、启动并执行对应业务验收。`GOWORK=off` 单独不能阻止
Builder 读取祖先 `go.work`，所以目录隔离也是必要步骤。
参考[发布流程](../RELEASE.md)和
[Core 发布协调](https://github.com/ingot-agent/ingot/blob/main/RELEASE.md)。

## 本次核验范围

2026-09-22 本机环境为 Windows/amd64、Go 1.26.3。验证使用全新测试 Home，
当前 Core/插件 checkout，以及显式 SDK/ABI 本地 replacement。
没有读取或修改日常使用的 Managed Home。

| 检查 | 结果与边界 |
|---|---|
| 三份完整 recipe | TOML 可解析、模块路径/本地目录/顺序一致；Core resolve 成功 |
| 三个 Image | 使用上文明确记录的依赖预取后，图校验、生成代码、编译与 Builder `--ingot-check` 通过；全新缓存的直接流程有已记录限制 |
| 无密钥启动 | 三个 Runtime 可启动；HTTP `/api/state` 可读取；测试后停止 |
| 教程模块 | 提取文档 Go 文件，`GOWORK=off` tidy/build/vet/test；工具边界用例通过 |
| 教程 Image | 三插件组合构建通过，真实运行输出问候与 Schema 拒绝提示，正常退出 |
| 未执行项 | 真实模型调用、模型驱动的编辑/审批/子代理整流程、跨平台/race、公开 tag 完整组合 |

上面的人工验收步骤明确区分“本次已执行”与“真实部署仍要执行”。Image 构建
通过只能证明构造与依赖有效；新建 Runtime 没有模型凭据是正常状态，不表示
已经完成模型服务联调。

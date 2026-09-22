# interceptor.script

`interceptor.script` 将可信的本机可执行文件组合到工具、模型、流式模型或 Agent 的调用前后，用于外部策略和审计。模块路径为 `github.com/ingot-agent/plugins/interceptor-script`，插件 ID 为 `interceptor.script`，组件为 `default`（包 `.`），兼容 Ingot `>=0.3.0 <0.4.0`，见 [manifest](ingot.plugin.toml)。

唯一依赖为 ABI `state.Scope`。导出 `[]tool.Interceptor`、`[]model.Interceptor`、`[]model.StreamInterceptor`、`[]agent.Interceptor` 及配置 Operations。各目标始终导出一个分发器，因此初始为空的配置也能通过 Operation 增加 hook，无须重建组件图。

## 配置与进程环境

配置位于插件自己的 Runtime state scope 的 `config.toml`。文件不存在时 hooks 为空，没有启用任何外部进程；未知 TOML 字段报错。以下是需要按实际路径修改的示例：

```toml
[[hooks]]
name = "check-tools"
target = "tool"
executable = "/absolute/path/to/policy-hook"
args = []
timeout_seconds = 10
max_output_bytes = 65536

[hooks.environment]
POLICY_MODE = "local"
```

每个 hook 的 name 必须是唯一、非空 UTF-8；target 为 `tool`、`model`、`model-stream` 或 `agent`。executable 必须为现有普通文件的绝对路径，实际可执行性在启动进程时由操作系统检查。args 为直接传递的参数数组，没有 Shell 展开；脚本需要解释器时把解释器作为 executable、脚本路径作为 args。

timeout 默认 10 秒，max_output 默认 64 KiB，0 选默认值，负数或 duration 溢出无效。stdout 和 stderr 分别受该上限约束，任一超限使 hook 失败。工作目录固定为 executable 所在目录，不是 Session Workspace。只显式传入 environment 表，不主动继承父进程环境；操作系统进程创建规则可能补充必要环境。键和值不得包含 NUL，键不得为空或含 `=`，Windows 按大小写不敏感判重。

配置的程序以宿主用户权限运行，可以访问 stdin 中的请求/结果和本机资源。环境筛选、超时和进程组/Job Object 清理不是安全沙箱；只配置可信程序。

## Hook protocol v2

每个阶段启动一次进程，stdin 是单个 UTF-8 JSON 对象，stdout 必须为单个响应对象。成功退出码必须为 0，**成功进程写出任何 stderr 也会失败**；stdout 不应混入日志。请求形状：

```json
{
  "protocol_version": 2,
  "hook": "check-tools",
  "target": "tool",
  "phase": "before",
  "request": {"id":"call-1","name":"read_file","arguments":{"path":"README.md"}}
}
```

before 成功返回：

```json
{"protocol_version":2,"action":"continue"}
```

before 拒绝返回：

```json
{"protocol_version":2,"action":"reject","message":"此调用不符合本地策略"}
```

after 额外包含 `outcome`：成功为 `{"response": <目标响应>, "error": null}`，失败为 `{"response":null,"error":{"kind":"...","message":"..."}}`。after 只允许 `continue` 且不能带 message。continue 同样不能带 message；reject 必须有非空 UTF-8 message。响应拒绝未知字段、重复字段、缺失字段、`message:null`、无效 UTF-8、多段 JSON 或其他协议版本。不支持修改请求或结果。

| target | request 内容 | 成功 response 内容 |
| --- | --- | --- |
| `tool` | `id`、`name`、原始对象 `arguments`；不包含 execution Scope | 有序 `content` |
| `model` / `model-stream` | `provider`、`model`、`messages`、`tools`、`temperature`、`max_tokens`、`stop` | `message`、`finish_reason`、`usage`、`provider`、`model` |
| `agent` | `session_id`、`input`、`attachments` | `output` |

内容 part 使用字符串 kind（`text`、`image`、`audio`、`video`、`file`）。媒体 source 使用 `inline`/`uri`/`asset`，内联 data 在 JSON 中为 base64，asset 投影为 ID；插件不自动读取 URI 或 Asset 内容。`model-stream` hook 包围整次 Stream 调用，原 handler 原样转发；不逐 delta 运行脚本。

## 顺序与失败

同目标 hook 按声明顺序进入 before，按相反顺序退出 after。before 拒绝或失败阻止其下游调用。after 在下游返回后执行；父 context 取消或下游已经返回取消/超时错误时跳过 after。

进程启动失败、超时、输出超限、非零退出、stderr 或协议错误均 fail closed。before 显式拒绝为 `ErrHookRejected`；其他 hook 失败为 `ErrHookFailed`。after 失败同时带 `ErrAfterHookFailed` 和 `ErrCompletionUnknown`，保留已有下游错误，表示下游可能已经产生副作用，不能因失败自动重试。该协议没有回滚能力。

## 配置操作与验证

Web 命令 `/interceptor-script config` 对应 Group `interceptor-script`、Name `config`、输入 `{}`。通过层级列表编辑 hooks、args 和环境条目。已有 hook 用 source 标识，环境值以 keep/replace/clear 动作处理；旧秘密值不作为表单默认值，移除条目表示删除。更新检查磁盘冲突，重新校验全部声明后原子保存并发布，返回 hook 数量与 `restart_required:false`。每次目标调用使用进入分发器时的 hook 快照，进行中的调用不随配置改变。直接改文件后需重启或通过 Operation 提交。

在本模块目录执行 `go test ./...`；发布依赖验证设置 `GOWORK=off`。测试覆盖四种目标的投影、严格 JSON 协议、环境隔离、超时/输出/stderr/退出码、before 拒绝、after 完成未知和配置实时增加 hook。

返回 [插件文档索引](../docs/README.md)。

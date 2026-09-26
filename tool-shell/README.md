# tool.shell

`tool.shell` 通过配置好的 Shell 执行一次命令，提供 `shell_exec`。模块路径为 `github.com/ingot-agent/plugins/tool-shell`，插件 ID 为 `tool.shell`，组件为 `default`（包 `.`），兼容 Ingot `>=0.3.0 <0.4.0`，见 [manifest](ingot.plugin.toml)。

依赖 `workspace.Resolver`、ABI `state.Scope` 和可选的 `observation.Consumer`；导出工具及配置 Operation。工作目录来自本次 `Invocation.Scope` 对应的 Session Workspace，没有工具参数或配置字段可以另设 cwd，也不会回退到进程当前目录。审批由独立拦截器提供。

## shell_exec

```json
{"command":"go test ./...","timeout_seconds":60}
```

`command` 必须是非空 UTF-8 字符串。可选的 `timeout_seconds` 是正整数，最多等于配置超时；省略时使用配置值。未知属性及尾随 JSON 会被拒绝。调用为一次性、非交互式执行，无 PTY、stdin 输入、后台任务 ID 或后续写入接口。

命令由选定 Shell 解释：PowerShell/pwsh 使用 `-Command`，Windows 的其他 Shell 使用 `/C`，其他平台使用 `-c`。配置 Windows 上的自定义 Shell 时必须确保支持 `/C`，不能假设任意 Shell 方言兼容。工作区是命令的初始目录，命令本身仍可 `cd` 或访问绝对路径。

成功和非零退出码均返回含 `exit_code`、`stdout`、`stderr` 的文本结果。插件自身超时在清理成功后返回退出码 `124` 的普通结果及超时说明；父 context 取消会作为错误传播并停止所属执行。超限输出被截断并标记 `[output truncated]`，不会仅因输出截断终止命令。输出预算固定分配：stdout 取得向上取整的一半，stderr 取得另一半，不会相互借用未使用的配额；结果标签和截断标记不计入原始捕获配额。

输出先规范化为 UTF-8。Windows 支持本机编码的解码；不完整 UTF-8 会跨写入块缓存，无法解码的内容使用替代字符。存在 Observation Consumer 时按 stdout/stderr 发布进度文本；这不是命令生命周期事实的替代，也不改变最终结果预算。

## 配置

插件读取自己 Runtime state scope 下的 `config.toml`，缺失时使用默认值。以下示例直接作为文件内容，不使用旧式 `[plugins."tool.shell"]` 外层表：

```toml
shell = ""
timeout_seconds = 120
max_output_bytes = 1048576
# 不写 inherit_env：继承完整父进程环境。
# inherit_env = []：不主动继承父进程环境。
# inherit_env = ["PATH"]：仅继承列出的父进程变量。

[environment]
LANG = "C.UTF-8"
```

| 字段 | 默认值及约束 |
| --- | --- |
| `shell` | 空值按平台选择；明确配置必须是存在的绝对可执行文件路径，错误不会自动回退 |
| `timeout_seconds` | 120 秒；0 用默认值，负数及溢出的 duration 无效 |
| `max_output_bytes` | 1 MiB；0 用默认值，负数无效 |
| `environment` | 显式变量表；值必须是无 NUL 的 UTF-8 |
| `inherit_env` | 缺省继承全部；空数组与缺省含义不同；非空数组中的变量必须在宿主环境存在 |

变量名必须匹配 `[A-Za-z_][A-Za-z0-9_]*`。继承全部时显式变量覆盖父进程同名变量；选定列表与显式表不能重名。Windows 按大小写不敏感判重。环境值在构造或配置提交时形成快照；POSIX 的完整继承模式还会把 `PWD` 调整为 Workspace Root。系统进程创建机制仍可能补充系统必需的环境变量，因此空列表不应被理解为安全隔离。

Unix 默认依次尝试当前用户 `/etc/passwd` 中的 Shell、`/usr/bin/zsh`、`/usr/bin/sh`、`/bin/sh`。Windows 优先 PowerShell 7（系统安装路径及 WindowsApps），再 Windows PowerShell，最后有效的 `ComSpec`/系统 `cmd.exe`。没有可用候选时构造失败。

Web 命令 `/tool-shell config`（Group `tool-shell`、Name `config`、输入 `{}`）支持 Shell、大小/时间限制、继承模式及环境表。环境条目使用 `source` 加 `keep`/`replace`/`clear` 动作：保留、替换、设为空字符串；移除条目才是删除变量。旧值不会作为交互默认值暴露，新值字段为敏感输入。保存检查冲突并原子发布，返回 `restart_required:false`；后续调用使用新配置，当前命令不被中途改写。手工改文件后需重启或通过 Operation 提交。

## 执行边界

Shell 与命令以宿主用户权限执行，Workspace、超时、环境筛选和进程清理都不构成文件系统或网络沙箱。默认继承环境可能包含凭据。需要审批时配置 [interceptor.approval](../interceptor-approval/README.md)，需要操作系统隔离时由部署环境提供。

Unix 使用进程组、Windows 使用 Job Object 处理后代进程；清理无法确认时返回 `ErrProcessCleanup`，不会将其报告为正常超时成功。进程启动失败、真实 I/O/清理失败与参数错误会向上传播。

在本模块目录执行 `go test ./...`；发布依赖验证可设置 `GOWORK=off`。测试涵盖默认 Shell、Windows 编码、超时/取消、固定流配额、环境继承/隔离、Workspace 作用域和配置秘密保留。

返回 [插件文档索引](../docs/README.md)。

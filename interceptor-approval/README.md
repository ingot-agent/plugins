# interceptor.approval

`interceptor.approval` 是工具调用前的审批策略插件。模块路径为 `github.com/ingot-agent/plugins/interceptor-approval`，插件 ID 为 `interceptor.approval`，组件为 `default`（包 `.`），兼容 Ingot `>=0.3.0 <0.4.0`，见 [manifest](ingot.plugin.toml)。

依赖 ABI `state.Scope` 和可选的 `interaction.ExecutionBinder`；导出 `[]tool.Interceptor`、`[]operation.Operation`，应把工具拦截器接到 [tool.runtime](../tool-runtime/README.md)。它不拦截模型请求、Operation 或绕过 tool.Runtime 的直接执行，也不提供系统沙箱。

## 策略与默认值

插件读取自己 Runtime state scope 的 `config.toml`；没有文件时默认对所有工具询问审批。文件直接包含以下配置，不加 `[plugins."interceptor.approval"]` 外层表：

```toml
default_action = "ask"
argument_display = "full"
max_display_bytes = 4096

[[rules]]
tool = "read_file"
action = "allow"

[[rules]]
tool = "shell_exec"
action = "ask"
```

| 字段 | 语义 |
| --- | --- |
| `default_action` | `allow`、`ask`、`deny`；空字符串默认 `ask` |
| `argument_display` | `full` 显示压缩 JSON 参数，`name-only` 只显示排序后的顶层参数名；空字符串默认 `full` |
| `max_display_bytes` | 参数展示部分的 UTF-8 字节上限，默认 4096；0 用默认值，负数无效 |
| `rules` | 以工具精确名称覆盖默认动作，不支持通配符、正则或参数匹配；同名规则重复无效 |

上例给 `read_file` 永久免询问权限，是可选策略示例，插件缺省配置不含这些 allow 规则。`full` 会把参数值展示给宿主，可能包含敏感文本；`name-only` 隐藏参数值但也减少用户判断依据。超长展示会在 UTF-8 边界截断，并附截断标记；只限制展示，不改写实际 Call 参数。

## 审批流程

`allow` 立即调用下游，`deny` 返回 `ErrApprovalDenied`。`ask` 用当前 `Invocation.Scope` 绑定 Channel，发出名为 `tool_approval`、级别为 warning 的交互，包含工具名、Call ID、参数展示和一个必填 `decision` choice：`allow`（Yes）或 `deny`（No）。

允许只覆盖当前一次调用，没有“永远允许”缓存；需要固定规则时修改配置。回答必须包含唯一字符串 decision。未知值、缺失/重复/类型错误回答最多再询问，累计最多三次，仍无有效允许则拒绝。用户拒绝立即结束。

Interaction 能力可在构造时缺失，便于 allow/deny-only 部署；实际遇到 ask 时会 fail closed，返回 `ErrApprovalUnavailable`/Interaction 错误，不会静默允许。绑定或请求失败以及父 context 取消也不会执行下游。审批由返回的 error 表达，不伪装为工具成功文本。

## 配置操作

Web 命令 `/interceptor-approval config` 对应 Group `interceptor-approval`、Name `config`、输入 `{}`。交互展示默认动作、展示模式、大小限制和可编辑的规则列表。更新执行同样的配置校验，检测交互期间磁盘配置变化，原子保存后立即发布到后续调用，返回 `{"restart_required":false}`。

当前已开始的审批使用进入拦截器时的策略快照；修改配置不会自动撤销已经发出的审批请求。直接修改磁盘文件需要重启或用配置 Operation 提交。未知 TOML 字段、非法动作/展示模式、重复规则会被拒绝，不会回落到默认放行。

在本模块目录执行 `go test ./...`；发布依赖验证设置 `GOWORK=off`。测试覆盖动作优先级、规则规范化、缺失/异常回答、执行作用域绑定、取消和实时配置。

返回 [插件文档索引](../docs/README.md)。

# tool.ask

`tool.ask` 提供 `ask_user` 工具，通过宿主的结构化 Interaction 向用户提问。模块路径为 `github.com/ingot-agent/plugins/tool-ask`，插件 ID 为 `tool.ask`，组件为 `default`（包 `.`）。兼容范围见 [manifest](ingot.plugin.toml)，当前为 Ingot `>=0.3.0 <0.4.0`。

## 组合与执行

构造器依赖 `interaction.ExecutionBinder` 和 ABI `state.Scope`；导出 `[]tool.Tool` 与 `[]operation.Operation`，通常分别连接到 `tool.runtime` 和应用宿主。每次调用使用 `Invocation.Scope` 绑定 Interaction Channel，不能用观察事件的 correlation 或进程全局 Channel 替代会话路由。绑定失败或宿主不可用会返回错误。

`ask_user` 的参数是 JSON 对象：

| 参数 | 必需 | 说明 |
| --- | --- | --- |
| `prompt` | 是 | 非空 UTF-8 问题文本 |
| `options` | 否 | 非空数组，每项包含必需的非空 `label` 和可选的 `description` |

```json
{"prompt":"请选择部署环境","options":[{"label":"测试环境","description":"用于发布验证"},{"label":"生产环境"}]}
```

选项标签必须唯一。选项是字符串输入的建议值，用户仍能输入自由文本；它不是只能从枚举中选择的审批框。未知属性、`options: null`、空选项数组和多段 JSON 会被拒绝。请求名为 `ask_user`，唯一回答字段为 `answer`；成功工具结果直接返回回答文本，不添加 JSON 包装。

## 配置

插件读取自身 Runtime state scope 下的 `config.toml`。文件不存在时使用默认值；未知 TOML 字段、读取或解码错误会使构造失败。此文件直接包含下列字段，不能包在旧式 `[plugins."tool.ask"]` 表中。

```toml
max_prompt_bytes = 16384
max_response_bytes = 16384
max_options = 8
max_options_bytes = 16384
```

四个值均为正整数；持久配置中的 `0` 表示使用对应默认值，负数无效。字节限制按 UTF-8 字节数计算，`max_options_bytes` 是全部标签和描述的累计字节数。

在 Web UI 执行 `/tool-ask config`（Operation 的 Group 为 `tool-ask`、Name 为 `config`，输入 `{}`）可交互修改上述四项。保存采用临时文件替换，并在提交前检查交互期间配置是否变化；冲突时不覆盖较新的配置。保存成功后后续调用立即使用新限制，返回 `{"restart_required":false}`；正在执行的调用保留开始时的配置快照。手动修改文件不会自动发布到运行中的实例，需要重启或通过配置 Operation 提交。

## 失败与边界

提示或选项超限、回答超限、重复/缺失/非字符串回答等已知业务失败以 `ask_user error: ...` 文本返回；参数结构错误、Channel 请求错误及执行取消以 Go error 返回。父 context 取消会保留取消语义，不会伪装成普通回答。该工具只收集输入，不代表对其他工具的授权；工具审批由独立的 [interceptor.approval](../interceptor-approval/README.md) 实现。

在本模块目录执行 `go test ./...`。若位于多仓库开发 workspace 中且要验证发布依赖，设置 `GOWORK=off` 后运行。测试覆盖选项/自由输入、执行作用域、大小边界、异常回答以及配置实时生效。

返回 [插件文档索引](../docs/README.md)。

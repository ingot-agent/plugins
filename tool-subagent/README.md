# tool.subagent

`tool.subagent` 将 SDK `agent.Children` 能力投影成模型可以调用的七个工具。模块路径为 `github.com/ingot-agent/plugins/tool-subagent`，插件 ID 为 `tool.subagent`，组件为 `default`（包 `.`），兼容 Ingot `>=0.3.0 <0.4.0`，见 [manifest](ingot.plugin.toml)。

唯一依赖为 `agent.Children`，导出 `[]tool.Tool`。该插件不持有 Session 存储、不调度子 Agent、不分配 Workspace，也没有私有 `config.toml` 或配置 Operation。Agent 类型、授权关系、并发和数量限制由所选 `agent.Children` 实现决定；官方实现见 [agent.default](../agent-default/README.md)。

## 工具接口

所有工具的 JSON Schema 均为对象，禁止未知属性；通过 [tool.runtime](../tool-runtime/README.md) 调用时先完成 Schema 校验。所有 Children 方法收到原样的 `Invocation.Scope`，提交结果还会传递当前 Call ID，不能靠传入别的 Session ID 获得额外权限。

| 工具 | 参数 | 行为 |
| --- | --- | --- |
| `list_agent_types` | `{}` | 查询当前执行可以创建的 Agent 类型 |
| `spawn_agent` | 必需 `agent_type`、`task`、`workspace_root`；可选 `context`、`wait` | 创建并运行一个单 Turn 的子 Session；三个必需字符串均非空，`wait` 默认 `true` |
| `check_agent` | 必需非空 `session_id`；可选 `include_result`（默认 `false`） | 读取当前状态，不等待完成 |
| `wait_agent` | 必需非空 `session_id`；可选 `timeout_ms`（1–3,600,000） | 等待本进程的当前执行；没有当前执行时读取一次持久状态 |
| `list_agents` | 可选 `parent_session_id`、`cursor`、`page_size`（1–100） | 查询一页直接子 Session；缺省父 Session/页大小的解释由 Children 实现负责 |
| `cancel_agent` | 必需非空 `session_id` | 请求取消子分支中排队或运行中的任务 |
| `submit_agent_result` | 必需非空 `result` | 提交当前子 Turn 的最终报告并结束该 Turn |

```json
{"agent_type":"reviewer","task":"检查本次修改的兼容性","workspace_root":"/absolute/path/to/project","wait":false}
```

`reviewer` 仅是示例，先查询 `list_agent_types` 使用真实配置的类型。Workspace Root 直接作为 `workspace.Binding` 交给 Children 实现做规范化和验证；工具本身不会创建 Git worktree 或复制代码。子 Agent 可以共享同一个目录，冲突控制需要任务编排方处理。

未提供 `wait_agent.timeout_ms` 时仅由父 context 或子执行完成结束等待。单次等待超时返回 `wait_timeout`，**不会取消子 Agent**；需要取消时显式调用 `cancel_agent`。

## 结果与错误

结果均为工具文本中的 JSON 字符串，而不是额外的结构化 Content 类型：

```json
{"ok":true,"child":{"session_id":"..."}}
```

上例仅展示包装形状，`child` 的完整字段由 SDK `agent.ChildSnapshot` 定义。成功查询分别返回 `types`、`child`、`page` 或 `cancel`；`submit_agent_result` 成功为 `{"accepted":true}`。业务失败通常为 `{"ok":false,"error":{"code":"...","message":"..."}}`，有可用快照时还包括 `child`。

错误码包括 `unsupported`、`unauthorized`、`capacity`、`invalid_state`、`invalid_submission`、`not_found`、`operation_failed`，以及本插件等待超时生成的 `wait_timeout`。父 context 已取消时保留 context error。`submit_agent_result` 的提交错误直接作为 error 返回，不包装成普通业务结果。

在本模块目录执行 `go test ./...`；设置 `GOWORK=off` 可验证发布依赖。测试覆盖固定工具集合、作用域与 Call ID 传递、类型查询、业务错误包装和取消传播。

返回 [插件文档索引](../docs/README.md)。

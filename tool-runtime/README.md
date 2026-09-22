# tool.runtime

`tool.runtime` 是工具定义、参数校验、拦截器调用和结果校验的统一入口。模块路径为 `github.com/ingot-agent/plugins/tool-runtime`，插件 ID 为 `tool.runtime`，唯一组件为 `default`（包 `.`），兼容 Ingot `>=0.3.0 <0.4.0`，见 [manifest](ingot.plugin.toml)。

依赖 `[]tool.Tool`、`[]tool.Interceptor` 和 ABI `state.Scope`；导出 `tool.Runtime` 与 `[]operation.Operation`。它本身不贡献模型可调用的工具，工具来自其他插件。定义注册表和拦截器集合在构造时确定，运行时配置只调整载荷限制，不动态增删工具。

## 注册与调用契约

构造时读取并复制每个工具的 Definition。工具名称必须匹配 `^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`，名称不能重复，描述必须是非空 UTF-8。InputSchema 必须是合法 JSON 对象；未声明 `$schema` 时默认 Draft 2020-12，显式声明其他 draft 会被拒绝。所有 Schema 在构造时编译，nil 工具或拦截器会使构造失败。

调用顺序如下：

1. 检查父 context、原始参数字节数、JSON 语法、工具是否存在。
2. 按工具 InputSchema 验证参数，复制 Call 参数并保留显式 execution Scope。
3. 按注入顺序执行拦截器：前面的拦截器在外层，正常返回按相反顺序退出。拦截器可以提前返回，也可以调用下游。
4. 在实际 `Tool.Invoke` 前确认 Scope、Call ID、工具名和参数字节均未被修改，再验证一次参数。
5. 校验成功结果的 Content 和大小限制，并向调用方返回独立的内容副本。

工具运行后的错误不会被转换成“可以安全重试”的前置拒绝。`tool.ErrNotFound` 和 `tool.ErrInvalidArguments` 是保留的 dispatch 前 sentinel；如果工具或拦截器在 dispatch 后返回这些错误，会转换为 `ErrPostDispatchRejection`，不再通过 `errors.Is` 暴露原 sentinel。Scope/Call 修改返回 `ErrCallMutation`。结果无效或超限返回 `ErrInvalidResult`。

结果校验发生在工具运行之后：它不会回滚已经产生的文件修改、进程或外部副作用。调用者不能因为结果超限而自动重试有副作用的工具。

## 配置与默认限制

插件读取自身 Runtime state scope 的 `config.toml`。缺失文件使用默认值；未知 TOML 字段或错误内容会使构造失败。字段直接位于文件顶层：

```toml
max_arguments_bytes = 1048576
max_text_bytes = 4194304
max_inline_part_bytes = 16777216
max_inline_bytes = 33554432
```

| 字段 | 默认值 | 计数范围 |
| --- | --- | --- |
| `max_arguments_bytes` | 1 MiB | 单次 Call 的原始 JSON 参数字节数 |
| `max_text_bytes` | 4 MiB | 单次结果全部文本 part 的累计 UTF-8 字节数 |
| `max_inline_part_bytes` | 16 MiB | 单个内联媒体 part 的原始数据字节数 |
| `max_inline_bytes` | 32 MiB | 单次结果全部内联媒体数据的累计字节数 |

URI 和 Asset 引用内容仍需通过 Content 校验，但这里不下载或计量其远端/存储内容。`0` 选取默认值，负数无效；两个内联限制分别生效，不要求累计限制大于单 part 限制。

Web 命令 `/tool-runtime config` 对应 Group `tool-runtime`、Name `config`、输入 `{}`，用结构化交互修改限制。保存时检查配置冲突，原子保存成功后立即发布到后续 Call，并返回 `{"restart_required":false}`。进行中的 Call 保留开始时的配置；直接编辑文件需要重新构造 Runtime 才会生效。

在本模块目录执行 `go test ./...`；设置 `GOWORK=off` 可验证模块声明的发布依赖。现有测试检查定义快照、JSON Schema、拦截器顺序、多模态计量、参数与作用域不可变性、dispatch 前后错误语义及实时配置。

返回 [插件文档索引](../docs/README.md)。

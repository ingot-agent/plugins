# tool.edit

`tool.edit` 提供工作区文本文件的 `edit_file`、`read_file` 和 `search`。模块路径为 `github.com/ingot-agent/plugins/tool-edit`，插件 ID 为 `tool.edit`，组件为 `default`（包 `.`）。兼容 Ingot `>=0.3.0 <0.4.0`，具体以 [manifest](ingot.plugin.toml) 为准。

依赖 `workspace.Resolver` 和 ABI `state.Scope`，导出 `[]tool.Tool` 和配置 `[]operation.Operation`。每次调用根据 `Invocation.Scope` 解析所属 Session 的 Workspace，未绑定或无法解析的 Session 会失败，不回退到进程当前目录。

## 工具参数和返回值

所有参数均为 JSON 对象，拒绝未知字段和尾随 JSON。文件内容必须是有效 UTF-8。

| 工具 | 必需参数 | 可选参数 | 行为 |
| --- | --- | --- | --- |
| `edit_file` | 非空 `path`、非空 `old` | `new`（默认空字符串）、`replace_all`（默认 `false`） | 精确子串替换；默认只替换第一处，空 `new` 表示删除匹配内容 |
| `read_file` | 非空 `path` | `start_line`、`end_line` | 无行号时返回整个文件；行号从 1 开始，两端包含 |
| `search` | 非空 `pattern` | `path`（默认工作区根目录）、`glob` | 递归按精确子串搜索，输出匹配行 |

```json
{"path":"src/main.go","old":"oldName","new":"newName","replace_all":true}
```

`edit_file` 仅修改已有文件，不创建文件或目录。未找到旧文本、旧新文本相同、目标为目录、文件不存在或超限会返回说明文本。成功返回 `edited`、`replacements`、`bytes` 三行摘要。写入使用同目录临时文件和 rename，并通过 `Chmod` 应用原文件 mode；权限语义由平台决定，Windows 不提供完整的 POSIX mode 保留。大小限制检查读取前的原文件大小，不是替换后文件大小的上限；也不提供并发编辑冲突检测。

`read_file` 的单边行号表示向另一方向开放范围。`end_line` 超过文件行数时截到末尾；起始行超过末尾、非正数行号或起始行大于结束行会返回错误说明。完整读取保留原始文本；行范围读取用换行连接所选行，不额外附加末尾换行。文件末尾换行不会多算一行；即使只读几行，仍先读取整个受大小限制的文件。

`search.path` 必须指向目录。`glob` 使用 Go `filepath.Match`，匹配文件基本名，例如 `*.go`，不是正则表达式或完整相对路径匹配。搜索跳过点开头的子目录/文件、非普通文件、超过单文件限制的文件和无效 UTF-8 文件，不读取 `.gitignore`。结果按文件名、行号排序，路径相对于本次搜索根目录。到达累计扫描字节限制时停止并明确标记结果截断；没有命中不是错误。预算按候选文件的 stat 大小累计，无效 UTF-8 文件也可能消耗预算。

## 路径与权限

工具拒绝绝对路径、`..` 路径段及清理后越出工作区的路径。该检查是路径规范约束，**不是文件系统沙箱**：不会对每个路径分量实施 symlink 隔离，也不阻止其他进程修改文件。工作区绑定只决定操作基准目录，实际访问权限仍由宿主进程和操作系统决定。需要人工批准写入时组合 [interceptor.approval](../interceptor-approval/README.md)。

路径策略拒绝、缺失文件、大小/编码限制和无命中等已知业务情况返回普通工具文本；真实 I/O 错误、参数格式错误和父 context 取消仍作为错误传播。

## 配置

插件自行读取其 Runtime state scope 下的 `config.toml`，没有文件时应用以下默认值：

```toml
max_file_bytes = 1048576
max_scan_bytes = 4194304
```

`max_file_bytes` 为单文件读取/编辑/搜索的预读大小阈值（1 MiB）；`max_scan_bytes` 为一次搜索累计文件大小预算（4 MiB）。持久文件的 `0` 采用默认值，负数无效；未知配置字段会报错。不要添加 `[plugins."tool.edit"]` 外层表。

Web 命令 `/tool-edit config` 的 Operation Group/Name 为 `tool-edit`/`config`，输入 `{}`。交互要求两个正整数，明确输入 `0` 也会被拒绝。成功原子保存并发布配置，返回有效的两个限制和 `restart_required:false`。交互期间持久配置变化会触发冲突，避免覆盖；每次工具调用只使用一个配置快照。直接编辑配置文件后需重启，或通过 Operation 提交。

在本模块目录执行 `go test ./...`；发布依赖验证时设置 `GOWORK=off`。测试覆盖替换次数、权限保留、路径/UTF-8/大小限制、行范围、搜索过滤、Workspace 作用域及配置交互。

返回 [插件文档索引](../docs/README.md)。

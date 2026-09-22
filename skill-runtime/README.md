# skill.runtime

`skill.runtime` 在插件自己的持久状态中发现、读取和创建 Agent Skills，向 Prompt 提供技能目录，向工具运行时提供 `read_skill`、`add_skill`。模块路径为 `github.com/ingot-agent/plugins/skill-runtime`，插件 ID 为 `skill.runtime`，组件为 `default`（包 `.`）。[manifest](ingot.plugin.toml) 声明 Ingot `>=0.3.0 <0.4.0`，state schema/min reader version 均为 1。

唯一依赖是 ABI `state.Scope`，要求其目录为非空绝对路径。导出 `[]prompt.Contributor`、`[]tool.Tool`、`[]operation.Operation`。没有可调 Config 或配置 Operation；限制是代码中的固定常量。

## 持久布局与格式

构造时创建 `<state.Scope.Dir()>/skills/`，不是 Session Workspace 下的 `.agents/skills`，也不扫描用户的 Codex 技能目录。每个 state 技能按以下布局存放：

```text
skills/
  review-release/
    SKILL.md
    references/
      checklist.md
      examples/example.txt
```

`SKILL.md` 必须是 UTF-8，以 YAML frontmatter 开始，并含非空正文：

```markdown
---
name: review-release
description: 检查版本发布材料与兼容性说明。
---

先核对实际实现，再检查公开文档中的命令、限制和迁移步骤。
```

`name` 必须与目录名相同，匹配 `^[a-z0-9]+(?:-[a-z0-9]+)*$`，长度 1–64。`description` 非空，最多 1024 个 Unicode 字符。可选元数据 `license`、`compatibility`、`allowed-tools` 会随读取结果展示；`allowed-tools` 仅为提示，不授予工具权限。插件只读取 `SKILL.md` 与 `references/` 下文本，不执行 `scripts/`、不加载 `assets/`。

参考文件路径相对于 `references/`，采用 `/` 分隔；不得为绝对路径、包含反斜杠、非规范 `.`/`..` 路径或点开头的段。技能目录、清单和参考文件/目录中的 symlink 会被拒绝；参考内容必须是普通 UTF-8 文件。点开头的目录项被扫描器忽略。

内置的 `skill-create` 技能直接嵌入二进制，来源为 `builtin`；该名称保留，磁盘文件和 `add_skill` 均不能覆盖。其他加载的技能来源为 `state`。

## 固定容量

| 限制 | 值 |
| --- | --- |
| 已接受技能数 | 64，包含内置技能 |
| 目录条目累计预算 | 48 KiB |
| 单个 `SKILL.md` | 64 KiB，包含 frontmatter |
| 每技能参考文件数 | 32 |
| 单个参考文件 | 64 KiB |
| 技能清单和参考内容总量 | 512 KiB |
| 参考子目录深度 | 4 层（最多 5 段的文件路径） |

目录预算按名称、转义后的描述及条目开销计量，不是所有技能正文的总量。正文按需通过工具读取，Prompt 只包含名称和描述，避免所有技能正文进入每次请求。

## 工具与诊断

`read_skill` 接收必需的 `name`，以及可选的非空 `reference`：

```json
{"name":"review-release"}
```

不指定 reference 时返回元数据、digest、来源、stale 状态、参考列表和正文；指定 `"reference":"checklist.md"` 时只返回该参考文件内容及标识。缺失技能或参考、非法路径以说明文本返回。

`add_skill` 接收 `name`、`description`、`instructions`，可选 `references` 数组，每项有 `path` 和 `content`。`instructions` 是正文，不应自行包 YAML frontmatter；插件生成清单。工具仅创建，已有目录（即使当前无法加载）或内置名称都会被拒绝，不覆盖、不更新已有技能。提交后技能可立即读取，下次 Prompt 目录刷新时可见。无效内容、重复参考、容量或名称冲突返回普通业务结果；I/O 和取消错误向上传播。

Web 命令 `/skill-runtime status` 对应 Group `skill-runtime`、Name `status`、输入 `{}`，返回 `root`、`generation`、`skills`、`rejected`、`last_refresh_error`。它只读取诊断，不需要交互填写配置。

每次目录贡献、read、add 或 status 都会刷新磁盘状态，不是后台 watcher。删除技能目录会移除条目；已加载技能的临时错误编辑会保留最后有效版本并标记 stale，新出现的无效技能会列入 rejected。初次根目录扫描失败会阻止构造；后续扫描失败保留上次快照并记录错误。generation 只在已发布状态变化时递增。这些快照只在当前进程内保留，不能用它恢复重启前的损坏技能。

在本模块目录执行 `go test ./...`；发布依赖验证设置 `GOWORK=off`。测试覆盖实时文件变化、stale/隔离行为、symlink、内置名称保留、create-only 并发、目录转义与取消。

返回 [插件文档索引](../docs/README.md)。

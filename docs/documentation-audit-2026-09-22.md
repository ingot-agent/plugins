# 文档修订与迁移记录：2026-09-22

本次按工作区当前源码核对开源文档。记录的是文档与本地实现的对应关系，
不表示工作区的新能力已经包含在公开发布的 module tag 中。

## 归属调整

| 内容 | 维护位置 | Core 中的处理 |
|---|---|---|
| 19 个官方插件的使用、配置、工具和能力 | 各模块 `README.md`，入口见[插件目录](../README.md#plugin-catalog) | Core 不再承载具体插件参考 |
| 16 份 v0.1 插件设计和子代理方案 | [design-history](design-history/README.md) | 旧文件及占位入口已删除 |
| SDK 设计方案 | [SDK 历史目录](https://github.com/ingot-agent/sdk/blob/main/docs/design-history/README.md) | 旧文件及占位入口已删除 |
| ABI 设计提案 | [ABI 历史目录](https://github.com/ingot-agent/ingot-abi/blob/main/docs/design-history/README.md) | 旧文件及占位入口已删除 |
| 通用 Builder、CLI、文件格式和 ADR | [Core 文档目录](https://github.com/ingot-agent/ingot/blob/main/docs/README.md) | 保留在 Core |

迁移的历史正文保留原设计内容、明确历史状态并链接当前实现说明。原始 Core
文档为 MIT 许可；迁入 Apache-2.0 的 SDK/ABI 仓库时附带原 MIT 许可和来源，
不改写其许可。SDK README 和贡献指南中与根 LICENSE 不符的 MIT 声明已修正。

## 已修复的公开说明

- Core 中英文概览、使用及贡献说明：默认和 minimal profile 都使用浏览器，
  分别选择 11/9 个模块；模型配置由模型插件负责，`app-webui config` 只负责宿主设置。
- 当前构造函数为 `New(ctx, deps)`，不接收旧 `Config`；Plugin 通过 `state.Scope`
  自己管理设置，不存在统一 `[plugins.<name>]` runtime 配置。
- `plugins.lock` v3 是 target-neutral；构建目标、工具链和构建参数进入 Image
  的构建事实，不是可写入 lock TOML 的字段。
- 当前目录有 19 个模块、16 个配置 Operation，其中 15 个在保存后发布运行时
  配置；WebUI 的服务器配置变更仍需要重启。`subagents.toml` 也在启动时加载。
- 模型 provider 使用 `ProviderSource` 快照。Agent 冻结自己的 Turn 设置，
  但没有显式覆盖的 model-runtime 默认值仍可能在后续模型轮次变化。
- Operation 允许空 Group 和重复显示名称；WebUI 通过 internal ID 路由。
- 补齐每个插件实际默认值、状态文件、工具参数、能力依赖、配置 Operation、
  使用边界，以及当前版本中缺失或不支持的能力。
- 更新插件开发、贡献和发布说明；区分当前源码、默认 profile 的 `v0.1.0`
  引用和真正的发布验证，历史提案不再作为现行 API 入口。

## 本地验证

验证环境为 Windows，Go `1.26.3`。仓库 CI 使用 Linux / Go `1.24.2`，因此
本次本地通过项不能替代对应 CI 环境和其他目标平台的发布验证。

| 检查 | 结果及范围 |
|---|---|
| 文档链接及 Markdown | 已检查四仓库本地链接、映射到本地检出的跨仓库 GitHub 路径、标题锚点及代码围栏；不代表未发布目标在远端已存在 |
| 当前 TOML 示例 | 35 个代码块通过 TOML 解析；字段、默认值和激活行为另与源码核对，历史提案不当作当前配置样例 |
| 插件仓库规则 | `scripts/validate_repo.py` 检查 19 个模块通过；11 个 Python CI helper 测试通过 |
| 插件 Go 测试 | 19 个模块均执行独立 `GOWORK=off go test`；18 个模块通过，`tool-edit` 的 Windows 权限测试失败，见下文 |
| SDK / ABI | 两仓库 `GOWORK=off go test -timeout 90s ./...` 通过 |
| Core CLI | 编译、帮助/JSON 版本，以及临时 Home/project 的 setup/init 检查通过；确认 default/minimal 的模块顺序和数量 |
| Web 前端 | lint、typecheck 和单元测试通过（14 个测试文件、65 个用例） |
| 差异格式 | 四仓库 `git diff --check` 通过 |

未执行完整跨平台 race suite、浏览器端到端、前端重新构建、真实模型请求、
Core 下载/自更新或已发布依赖的完整镜像构建。外部 DeepSeek 校准未调用。
这些检查仍应按各仓库发布规则执行，本文不将其记为通过。

## 发布前仍需处理的实现问题

1. **Windows 文件权限测试**：`tool-edit` 的 `TestEditPreservesPermissions`
   在本机得到 mode `0666`，测试期望 `0755`。本次只改文档，相关实现和测试
   未改；应明确平台权限承诺并修正实现或测试后再完成对应平台发布验证。
2. **Compactor 与 Responses 适配器组合**：当前 `context-compact` 摘要请求
   设置非 nil 的空 `Stop`，而 Responses 适配器拒绝任何非 nil `Stop`。
   此组合存在请求兼容性限制，已在[Compactor](../context-compact/README.md)
   和 [Responses](../model-openai-responses/README.md) 说明，需单独实现修复和组合验证。
3. **ABI 历史测试样例**：ABI `contracts_test.go` 仍将三参数普通函数样例
   称为 Builder 构造约定，但没有调用 Builder 校验。它编译通过并不能证明
   Builder 接受该签名；当前公开参考已改成双参数，测试样例应后续同步。

此外，Core profiles 和本仓库 `collections.toml` 当前固定 `v0.1.0`。
完成新插件及 SDK 发布后，必须显式更新需要升级的 recipe，并用真实发布版本
构建验证，不能把本地 workspace 替换下的成功当作发布依赖验证。

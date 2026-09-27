# Plugin documentation / 插件文档目录

This repository owns official plugin usage, configuration, implementation
behavior and design history. The [catalog](../README.md#plugin-catalog) links
the current reference for all **19** plugin modules.

具体插件文档已从 Core 迁入本仓库。当前 README 对应本分支源码；使用已发布
module tag 时，应阅读同一 tag 的文档，不能把分支上的新功能视为已发布。

| Task / 任务 | Reference / 文档 |
|---|---|
| Choose plugins / 选择插件 | [Plugin catalog](../README.md#plugin-catalog) |
| Configure a running agent / 首次配置 | [Configuration guide](configuration.md) |
| Build complete compositions / 完整组合 | [Recipes: chat, editing with approval, child agents](recipes.md) |
| Browser and HTTP API / 浏览器及接口 | [app-webui](../app-webui/README.md) |
| Inline follow-up persistence / 原文追问存储方案 | [Session Meta design](inline-followups-session-metadata-design.md) |
| Develop a plugin / 开发插件 | [Plugin development](plugin-development.md) |
| Write and actually call the first tool / 首个插件实操 | [First plugin tutorial](tutorials/first-plugin.md) |
| Activation and snapshots / 热更新边界 | [Live configuration](live-plugin-configuration.md), [Model providers](live-model-provider-configuration.md) |
| Implement configuration forms / 配置交互规范 | [Operation + Interaction conventions](plugin-configuration-interaction-conventions.md) |
| Checks and releases / 检查与发布 | [Contributing](../CONTRIBUTING.md), [Release process](../RELEASE.md) |
| Report security issues / 安全报告 | [Security policy](../SECURITY.md) |
| Older decisions / 历史设计与迁移映射 | [Design history](design-history/README.md) |
| September 16 findings / 历史审计 | [Configuration audit](official-plugin-configuration-audit-2026-09-16.md) |
| Documentation migration and verification / 本次迁移与验证 | [September 22 documentation audit](documentation-audit-2026-09-22.md) |

## Repository ownership / 仓库职责

| Repository | Owns |
|---|---|
| [Core](https://github.com/ingot-agent/ingot/blob/main/docs/README.md) | CLI, Builder, graph resolution, generic manifests, recipes, locks, images, runtimes, processes, Collections and ADRs |
| This repository | Concrete plugin behavior, defaults, private state, application protocols, frontend assets and per-module release instructions |
| [SDK](https://github.com/ingot-agent/sdk) | Optional public capability contracts and their ownership/concurrency semantics |
| [ABI](https://github.com/ingot-agent/ingot-abi) | Fixed Component wrappers and runtime-owned Invocation, Lifecycle and State contracts |

Historical designs preserve reasoning at the time of writing; they are not
current API specifications. Update the owning module README with behavior
changes and link to the enforcing code. Update shared guides when activation,
compatibility or composition changes.

Core 中已删除迁出文档及占位文件，完整正文只在所属仓库维护。

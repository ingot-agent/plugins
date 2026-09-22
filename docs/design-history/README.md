# Plugin design history / 插件设计历史

这里保留早期设计和实现过程，用于理解决策背景。**安装、配置和调用请使用各插件当前 README，不能直接复制历史配置和 API。**

## 来源与所有权

16 份 v0.1 设计和子代理设计于 2026-09-22 从 `ingot-agent/ingot` 的 `docs/` 迁移。原始版本可在 [Core commit 6739c7c](https://github.com/ingot-agent/ingot/tree/6739c7cb5e90560b75bb39d631c926bc1587c878/docs) 追溯；原文和本仓库均采用 MIT 许可。Core 中的旧文件和迁移占位页已删除。

这些文档中的 Implemented、里程碑和版本指原撰写时的范围，不证明全部描述仍与当前源码一致。共同变化包括：`New(ctx, deps)` 构造函数、Plugin 私有 state 配置、ProviderSource、Execution outcome、浏览器工作台，以及新增 skill/subagent 能力。

## 文档映射

| Manifest name | 当前参考 | 历史设计 |
|---|---|---|
| `agent.default` | [agent-default](../../agent-default/README.md) | [agent.default_v0.1.md](./agent.default_v0.1.md) |
| `app.backend` | [app-webui](../../app-webui/README.md) | [app.backend_v0.1.md](./app.backend_v0.1.md) |
| `asset.local` | [asset-local](../../asset-local/README.md) | [asset.local_v0.1.md](./asset.local_v0.1.md) |
| `context.compact` | [context-compact](../../context-compact/README.md) | [context.compact_v0.1.md](./context.compact_v0.1.md) |
| `http.default` | [http-default](../../http-default/README.md) | [http.default_v0.1.md](./http.default_v0.1.md) |
| `interceptor.approval` | [interceptor-approval](../../interceptor-approval/README.md) | [interceptor.approval_v0.1.md](./interceptor.approval_v0.1.md) |
| `interceptor.script` | [interceptor-script](../../interceptor-script/README.md) | [interceptor.script_v0.1.md](./interceptor.script_v0.1.md) |
| `model.openai-compatible` | [model-openai-compatible](../../model-openai-compatible/README.md) | [model.openai-compatible_v0.1.md](./model.openai-compatible_v0.1.md) |
| `model.runtime` | [model-runtime](../../model-runtime/README.md) | [model.runtime_v0.1.md](./model.runtime_v0.1.md) |
| `prompt.default` | [prompt-default](../../prompt-default/README.md) | [prompt.default_v0.1.md](./prompt.default_v0.1.md) |
| `session.sqlite` | [session-sqlite](../../session-sqlite/README.md) | [session.sqlite_v0.1.md](./session.sqlite_v0.1.md) |
| `tool.ask` | [tool-ask](../../tool-ask/README.md) | [tool.ask_v0.1.md](./tool.ask_v0.1.md) |
| `tool.edit` | [tool-edit](../../tool-edit/README.md) | [tool.edit_v0.1.md](./tool.edit_v0.1.md) |
| `tool.runtime` | [tool-runtime](../../tool-runtime/README.md) | [tool.runtime_v0.1.md](./tool.runtime_v0.1.md) |
| `tool.shell` | [tool-shell](../../tool-shell/README.md) | [tool.shell_v0.1.md](./tool.shell_v0.1.md) |
| `usage.default` | [usage-default](../../usage-default/README.md) | [usage.default_v0.1.md](./usage.default_v0.1.md) |
| 子代理 | [agent-default](../../agent-default/README.md) / [tool-subagent](../../tool-subagent/README.md) | [轻量版方案](./ingot_subagent_轻量版设计方案.md) |

`model-openai-responses`、`skill-runtime` 和 `tool-subagent` 的当前文档直接维护在对应模块 README。这里不补造缺失的历史版本。

返回[文档目录](../README.md)。

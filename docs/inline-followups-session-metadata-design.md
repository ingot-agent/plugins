# 原文追问改用 Session Meta 的设计方案

状态：已实施（未迁移旧数据）。范围：`sdk`、`session-sqlite`、`app-webui`。

## 目标与边界

追问仍是从主会话 fork 出来的普通 Session，继承 fork 时的完整历史和 Workspace Binding，之后可以连续进行多轮 Turn。便签的锚点和主从关系随追问 Session 一起持久化，重启后可以重新发现，不再维护 `inline-followups.json`。

借鉴子 Agent 将关系数据放进 Session Meta 的做法，但**不复用** `agent.kind=subagent`、`agent.Children`、单 Turn 限制或任务状态机。追问不应成为子 Agent，也不应写入模型上下文。现有 HTTP 请求、响应字段及前端便签交互保持不变。

## 现状与问题

- `app-webui/app/followups_http.go` 在 fork 后读取子会话历史条数，再把锚点和 `baseMessageCount` 写入 WebUI 状态目录的 `inline-followups.json`。
- `app-webui/app/followups.go` 负责便签索引、侧边栏隐藏和重启后的恢复。Session 与索引分两步提交，必须用补偿删除处理写入失败。
- `sdk/session.ForkRequest` 只接受标题；`session-sqlite.Fork` 已在事务中复制 Entries 和 Workspace Binding，但将目标 Meta 固定为 `{}`。
- `session-sqlite.List` 只排除 `agent.kind=subagent`；普通 Session 的 `Meta` 已随 `Query.List` 和 `Manager.Get` 返回，但 `app-webui` 的 `appbackend.Session` 投影不包含 Meta。

## 元数据结构

在**追问 Session 自己**的 `Meta["app-webui"]` 下保存 WebUI 私有数据。例如：

```json
{
  "app-webui": {
    "kind": "inline-followup",
    "sourceSessionId": "c0_parent",
    "anchor": {
      "messageIndex": 3,
      "partIndex": 0,
      "start": 12,
      "end": 26,
      "quote": "被选中的原文"
    },
    "baseMessageCount": 8
  }
}
```

`id`、`createdAt` 从 Session Metadata 取得，不重复存储。`baseMessageCount` 是 fork 时继承的模型消息数量，前端据此隐藏便签中的主会话历史。`kind` 用于识别，`sourceSessionId` 用于按主会话枚举及删除；不占用子 Agent 的 `agent` 命名空间。只有 WebUI 解析该命名空间，未知命名空间保持原样。带追问标记但字段无效的元数据不能被当作普通会话悄悄显示：列表和删除操作应明确报错，避免暴露便签或产生孤儿记录。

## 接口与实现改动

### SDK：允许 fork 原子写入目标 Meta

给 `session.ForkRequest` 增加 `Meta session.Meta`，约定 nil 表示空对象；目标不继承源 Session 的 Meta。这是现有 `CreateRequest.Meta` 的同类能力，不增加普通 Session 的任意元数据更新接口。已有只传 `Title` 的调用行为不变。

`session-sqlite.Fork` 在同一事务中编码并插入请求的 Meta、复制 Entries 与 Workspace Binding；返回的 Metadata 包含 Meta 的独立副本。Meta 编码失败时不得留下目标 Session。普通 fork 仍产生空 Meta，不能自动继承追问标记。

### app-webui：从 Session 查询重建便签

在 `sessionController` 内保留访问 `session.Metadata` 的内部路径，提供创建追问、按主会话列出追问、按 ID 取得追问的操作。由控制器解析/校验 `Meta["app-webui"]` 并投影成现有的 `followup` 响应；不要把 Meta 塞进公开的 `appbackend.Session` 或散落在 HTTP handler 中解析。

第一版复用 `session.Query.List`：一次获取所有普通 Session 的 Metadata，将带追问标记的记录从 `/api/sessions` 侧边栏列表中排除，并按 `sourceSessionId` 筛出 `/api/sessions/{id}/followups`。SQLite 的 `List` 不新增追问过滤，否则现有 Query 无法再发现便签。该方案列表成本为 O(N)，适合当前本地开发规模；将来确有性能问题时，再新增按父 ID 的查询与 SQLite 表达式索引。

创建时仍验证来源会话、所选 assistant 文本及运行状态；追问 Session 不能再次作为追问来源，避免产生嵌套便签。在 `sessionMu` 内取得来源历史长度，构造锚点 Meta 后调用带 Meta 的 fork。成功即发布 `followup.created` 并返回原有 `followup` 字段；不再写文件，也不需要索引写入失败后的补偿删除。打开、关闭、重新打开和继续提问仍使用同一个追问 Session ID。

这里的历史边界沿用 SDK `Fork` 的并发约定：WebUI 须禁止来源 Turn 正在运行，并在同一互斥区内完成历史计数与 fork；其他绕开 WebUI 的写入者若并发修改来源，`baseMessageCount` 可能与实际 fork 边界不一致。若未来要求跨入口的严格原子历史边界，需要额外的持久层/历史能力，本次不扩展该合同。

### 删除与关系完整性

`DELETE /api/followups/{id}` 从 Session Meta 校验目标确为追问后删除 Session，不再更新旁路索引。`DELETE /api/sessions/{id}` 先枚举并删除该主会话的追问，再删除主会话；保留运行中追问不可删除的检查和现有事件。中途失败时返回错误，重试可处理剩余会话。

为防止绕开 WebUI 直接删除主会话，`session-sqlite.Delete` 在现有子 Agent 子会话检查旁增加追问引用检查：若仍有 `app-webui.kind=inline-followup` 且 `sourceSessionId` 指向目标，拒绝删除。这样不引入级联删除，也不改变子 Agent 的删除规则。直接删除追问 Session 不需要修改主会话 Meta，因为关系只保存在追问侧。

## 不迁移旧 JSON

移除 `followupIndex`、启动时的 `openFollowupIndex`、JSON 文件读写及对应测试。启动时完全忽略已有 `inline-followups.json`，不做读取、导入、自动删除或一次性迁移。旧便签关系会丢失；此前 fork 出的 Session 因没有新 Meta，可能作为普通会话出现在列表中，开发环境可手动删除。不要删除用户机器上的旧文件作为升级步骤。

同步更新 `app-webui/README.md` 对存储方式的说明。前端类型和接口字段不变，原则上不需要 UI 改动，也不应顺带重建无关的 Web 静态产物。

## 验收与测试

1. SDK/SQLite：空 Meta 的普通 fork 行为不变；带 Meta 的 fork 原子保存目标 Meta、不继承源 Meta，并正确复制历史和 Workspace Binding；失败不留下半成品。
2. WebUI：创建即能列出便签，重建 application 后仍可列出并打开；侧边栏只显示主会话；多轮追问的历史按 `baseMessageCount` 展示。
3. 删除：删除单个便签、删除主会话及其多个便签、运行中拒绝删除、外部直接删除仍被引用的主会话均符合预期；重复删除/部分完成后重试行为明确。
4. 兼容性：普通 fork 不带追问标记；子 Agent 的列表、状态和删除测试保持通过；原 HTTP 响应结构及前端交互测试不变。

实现已按上述顺序落地。旧文件无需删除，也不会在启动时读取。

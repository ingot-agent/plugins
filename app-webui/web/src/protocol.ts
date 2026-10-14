export type Status = 'running' | 'succeeded' | 'failed' | 'canceled'
export type TurnStatus = Status | 'queued'
export interface Session {
  id: string
  title: string
  workspace?: string
  createdAt: string
  updatedAt: string
  totalToken: number
  archivedAt?: string
}
export interface WorkspaceSelection { path: string | null }
export interface ContextUsage {
  sessionId: string
  inputTokens: number
  accuracy: 'exact' | 'upper_bound' | 'estimate'
  source: string
  provider: string
  model: string
  turnId?: string
  roundIndex?: number
}
export interface Part {
  kind: string
  text?: string
  mimeType?: string
  name?: string
  source?: { kind: string; data?: string; uri?: string; assetId?: string; path?: string }
}
export interface ToolCall { id: string; name: string; arguments: unknown }
export interface Message {
  role: string
  content: Part[]
  name?: string
  toolCallId?: string
  toolCalls?: ToolCall[]
}
export interface Attachment { kind: string; mimeType?: string; name?: string; assetId?: string; path?: string; size?: number }
export interface FollowupAnchor { messageIndex: number; partIndex: number; start: number; end: number; quote: string }
export interface Followup extends FollowupAnchor {
  id: string
  sourceSessionId: string
  baseMessageCount: number
  createdAt: string
}
export interface Scope {
  agent?: { sessionId?: string; turnId?: string; roundIndex?: number; toolCallId?: string }
  operation?: { invocationId: string }
}
export interface ErrorDetail { code: string; message: string }
export interface Outcome {
  status: Status
  durationNs: number
  failure?: { stage: string; roundIndex?: number; toolCallId?: string }
}
export interface Turn {
  id: string
  sessionId: string
  revision: number
  reasoning: string
  output: string
  status: TurnStatus
  started: boolean
  sequence: number
  requestId?: string
  input: string
  attachments?: Attachment[]
  blocks: TurnBlock[]
  persisted: boolean
  retry?: { attempt: number; delayMs: number }
  sdkTurnId?: string
  historyEnd?: number
  outcome?: Outcome
  result?: { output: Part[] }
  error?: ErrorDetail
}
export interface TurnQueue {
  sessionId: string
  activeId?: string
  paused: boolean
  revision: number
}
export type TurnBlock =
  | { id: string; kind: 'output' | 'reasoning'; text: string; boundary: number; content?: Part[] }
  | { id: string; kind: 'tool'; call: ToolCall; status: string; content?: Part[]; error?: string }
  | { id: string; kind: 'interaction'; interactionId: string }
export interface LiveTurn extends Turn {
  stopping?: boolean
  reconciled?: boolean
}
export interface InteractionField {
  name: string
  label?: string
  description?: string
  kind: 'string' | 'integer' | 'number' | 'boolean' | 'choice' | 'multichoice' | 'object' | 'list'
  required: boolean
  sensitive: boolean
  hasDefault: boolean
  default?: unknown
  options?: { value: string; label?: string; description?: string }[]
  // Object fields carry their members; list fields carry one element
  // descriptor. Both nest, so renderers must recurse.
  fields?: InteractionField[]
  element?: InteractionField
}
export interface Interaction {
  id: string
  name: string
  scope?: Scope
  description?: string
  level?: string
  fields: InteractionField[]
}
export interface InteractionState {
  id: string
  name: string
  scope?: Scope
  level?: string
  description?: string
  values: { name: string; label?: string; description?: string; value: unknown }[]
}
export interface Schema {
  type?: string | string[]
  properties?: Record<string, Schema>
  required?: string[]
  enum?: unknown[]
  title?: string
  description?: string
  default?: unknown
  [key: string]: unknown
}
export interface Operation {
  id: string
  name: string
  description: string
  group: string
  inputSchema: Schema
  outputSchema: Schema
}
export interface OperationInvocation {
  id: string
  operationId: string
  name: string
  sessionId?: string
  status: Status
  result?: { output: unknown }
  error?: ErrorDetail
}
export interface Snapshot {
  cursor: number
  runtimeId?: string
  agent: { capabilities: { sendMessage: boolean } }
  assets?: { available: boolean; maxBytes: number }
  files?: { available: boolean; maxBytes: number }
  workspace?: { defaultPath: string }
  sessions: Session[]
  turns: Turn[]
  turnQueues?: TurnQueue[]
  interactions: Interaction[]
  interactionStates: InteractionState[]
  operations: Operation[]
  operationInvocations: OperationInvocation[]
}
export interface ModelSelection {
  provider: string
  model: string
  reasoningEffort: string
}
export interface ModelSelectionSnapshot {
  revision: string
  configured: boolean
  current: ModelSelection
  providers: { name: string; models: { name: string; reasoningEfforts: string[] }[] }[]
}
// Observation and Operation payloads deliberately include arbitrary plugin JSON.
export interface WebEvent { type: string; scope?: Scope; data: Record<string, any> }
export interface TraceEvent extends WebEvent { cursor: number }
export interface Notice { id: number; message: string; level: string; scope?: Scope }

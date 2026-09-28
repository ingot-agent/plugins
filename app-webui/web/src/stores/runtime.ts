import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { APIError, command, errorMessage, isAbort, request, segment } from '../api'
import { subscribe } from '../sse'
import { bootstrapTurns, indexById, reduceOperation, reduceTurn } from '../state'
import { equalInteractionValue } from '../forms'
import type { Attachment, Followup, FollowupAnchor, Interaction, InteractionState, LiveTurn, Message, ModelSelection, ModelSelectionSnapshot, Notice, Operation, OperationInvocation, Session, Snapshot, TraceEvent, WebEvent, WorkspaceSelection } from '../protocol'

export const useRuntime = defineStore('runtime', () => {
  const sessions = ref<Session[]>([])
  const followups = ref<Record<string, Followup[]>>({})
  const capabilities = ref({ run: false, stream: false })
  const assets = ref({ available: false, maxBytes: 0 })
  const defaultWorkspace = ref('')
  const turns = ref<Record<string, LiveTurn>>({})
  const interactions = ref<Record<string, Interaction>>({})
  const interactionDrafts = ref<Record<string, Record<string, unknown>>>({})
  const suspendedOperations = ref<Record<string, boolean>>({})
  const interactionStates = ref<Record<string, InteractionState>>({})
  const operations = ref<Operation[]>([])
  const modelSelection = ref<ModelSelectionSnapshot | null>(null)
  const operationInvocations = ref<Record<string, OperationInvocation>>({})
  const histories = ref<Record<string, Message[]>>({})
  const historyLoading = ref<Record<string, boolean>>({})
  const historyErrors = ref<Record<string, string>>({})
  const optimistic = ref<Record<string, { sessionId: string; message: Message }>>({})
  const traces = ref<Record<string, TraceEvent[]>>({})
  const notices = ref<Notice[]>([])
  const connection = ref<'connecting' | 'online' | 'reconnecting'>('connecting')
  const connectionError = ref('')
  const activeSession = ref('')
  const cursor = ref(0)
  let lifecycle: AbortController | undefined
  let epoch = 0
  let sessionRevision = 0
  let modelSelectionRevision = 0
  let noticeId = 0
  const dismissedOperations = new Set<string>()
  const historyRequests = new Map<string, AbortController>()
  const orderedSessions = computed(() => [...sessions.value].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt)))
  const pendingCount = computed(() => Object.values(interactions.value).filter(item => {
    const id = item.scope?.operation?.invocationId
    return !id || suspendedOperations.value[id]
  }).length)
  const pendingOperationRequests = computed(() => Object.values(interactions.value).filter(item => {
    const id = item.scope?.operation?.invocationId
    return !!id && !!suspendedOperations.value[id]
  }))

  function notify(message: string, level = 'error', scope?: Notice['scope']) {
    notices.value.push({ id: ++noticeId, message, level, scope })
    notices.value = notices.value.slice(-30)
  }
  function running(sessionId: string) {
    return Object.values(turns.value).filter(turn => turn.sessionId === sessionId && turn.status === 'running')
  }
  async function loadHistory(id: string) {
    if (!id) return
    historyRequests.get(id)?.abort()
    const controller = new AbortController()
    historyRequests.set(id, controller)
    historyLoading.value[id] = true
    delete historyErrors.value[id]
    const generation = epoch
    try {
      const messages = await request<Message[]>('/sessions/' + segment(id) + '/history', { signal: controller.signal })
      if (generation !== epoch || controller.signal.aborted) return
      histories.value[id] = messages || []
      for (const turn of Object.values(turns.value)) {
        if (turn.sessionId === id && turn.status !== 'running' && !turn.reconciled) {
          turn.reconciled = true
          turn.historyEnd = messages?.length || 0
          delete optimistic.value[turn.id]
        }
      }
    } catch (error) {
      if (!isAbort(error) && generation === epoch) historyErrors.value[id] = errorMessage(error)
    } finally {
      if (historyRequests.get(id) === controller) {
        historyLoading.value[id] = false
        historyRequests.delete(id)
      }
    }
  }
  async function refreshSessions() {
    const generation = epoch
    const revision = ++sessionRevision
    try {
      const items = await request<Session[]>('/sessions')
      if (generation === epoch && revision === sessionRevision) sessions.value = items || []
    } catch (error) { if (!isAbort(error)) notify(errorMessage(error)) }
  }
  function bootstrap(snapshot: Snapshot) {
    epoch++
    dismissedOperations.clear()
    sessionRevision++
    modelSelectionRevision++
    modelSelection.value = null
    void refreshModelSelection()
    for (const controller of historyRequests.values()) controller.abort()
    historyRequests.clear()
    histories.value = {}
    followups.value = {}
    historyLoading.value = {}
    historyErrors.value = {}
    cursor.value = snapshot.cursor
    sessions.value = snapshot.sessions || []
    capabilities.value = snapshot.agent.capabilities
    assets.value = snapshot.assets || { available: false, maxBytes: 0 }
    defaultWorkspace.value = snapshot.workspace?.defaultPath || ''
    turns.value = bootstrapTurns(snapshot)
    const previousInteractions = interactions.value
    const previousDrafts = interactionDrafts.value
    const previousSuspended = suspendedOperations.value
    interactions.value = indexById(snapshot.interactions)
    interactionDrafts.value = Object.fromEntries(snapshot.interactions.flatMap(item => {
      const previous = previousInteractions[item.id]
      return previousDrafts[item.id] && previous?.scope?.operation?.invocationId === item.scope?.operation?.invocationId &&
        equalInteractionValue(previous.fields, item.fields) ? [[item.id, previousDrafts[item.id]]] : []
    }))
    suspendedOperations.value = Object.fromEntries(snapshot.interactions.flatMap(item => {
      const id = item.scope?.operation?.invocationId
      return id ? [[id, previousInteractions[item.id] ? !!previousSuspended[id] : true]] : []
    }))
    interactionStates.value = indexById(snapshot.interactionStates)
    operations.value = snapshot.operations || []
    operationInvocations.value = indexById(snapshot.operationInvocations)
    // Process-local identifiers may be reused after a server restart.
    traces.value = {}
    optimistic.value = {}
    if (activeSession.value) {
      void loadHistory(activeSession.value)
      void loadFollowups(activeSession.value).catch(error => notify(errorMessage(error)))
    }
  }
  function receive(id: number, event: WebEvent) {
    if (id <= cursor.value) return
    cursor.value = id
    const data = event.data || {}
    event.data = data
    reduceTurn(turns.value, event)
    if (event.type === 'model.selection.updated') {
      modelSelectionRevision++
      modelSelection.value = data as ModelSelectionSnapshot
    } else if (event.type.startsWith('agent.invocation.') || event.type === 'agent.output.delta' || event.type === 'agent.reasoning.delta') {
      if (event.type === 'agent.invocation.finished') {
        const sid = event.scope?.agent?.sessionId
        if (sid && (sid === activeSession.value || histories.value[sid])) void loadHistory(sid)
        void refreshSessions()
        const settled = Object.values(turns.value).filter(turn => turn.status !== 'running')
        for (const turn of settled.slice(0, -128)) delete turns.value[turn.id]
      }
    } else if (/^agent\.(turn|round|model|tool)\./.test(event.type)) {
      const sid = event.scope?.agent?.sessionId
      if (sid) traces.value[sid] = [...(traces.value[sid] || []), { ...event, cursor: id }].slice(-500)
    } else if (event.type.startsWith('session.')) {
      sessionRevision++
      if (event.type === 'session.deleted') {
        sessions.value = sessions.value.filter(session => session.id !== data.id)
        delete histories.value[data.id]
        delete followups.value[data.id]
        for (const id of Object.keys(followups.value)) followups.value[id] = followups.value[id].filter(note => note.id !== data.id)
        historyRequests.get(data.id)?.abort()
      } else {
        const item = data as Session
        sessions.value = [...sessions.value.filter(session => session.id !== item.id), item]
      }
    } else if (event.type === 'followup.created' || event.type === 'followup.deleted') {
      const id = data.sourceSessionId
      if (id && (id === activeSession.value || followups.value[id])) void loadFollowups(id)
    } else if (/^operation\.(started|completed|failed|canceled)$/.test(event.type)) {
      reduceOperation(operationInvocations.value, event)
      if (event.type !== 'operation.started') clearOperationDrafts(data.id)
      if (event.type === 'operation.completed') void refreshModelSelection()
      const settled = Object.values(operationInvocations.value).filter(item => item.status !== 'running')
      for (const item of settled.slice(0, -128)) delete operationInvocations.value[item.id]
    } else if (event.type === 'interaction.requested') {
      if (!data.scope?.operation?.invocationId || !dismissedOperations.has(data.scope.operation.invocationId)) {
        interactions.value[data.id] = data as Interaction
      }
    } else if (event.type === 'interaction.resolved' || event.type === 'interaction.canceled') {
      const invocationId = interactions.value[data.id]?.scope?.operation?.invocationId
      delete interactions.value[data.id]
      delete interactionDrafts.value[data.id]
      if (invocationId && !Object.values(interactions.value).some(item => item.scope?.operation?.invocationId === invocationId)) {
        delete suspendedOperations.value[invocationId]
      }
    } else if (event.type === 'interaction.state.set') {
      interactionStates.value[data.id] = data as InteractionState
    } else if (event.type === 'interaction.state.clear') {
      delete interactionStates.value[data.id]
    } else if (event.type === 'interaction.event') {
      notify(data.message || data.name, data.level || 'info', event.scope)
    }
  }
  async function refreshModelSelection() {
    const generation = epoch
    const revision = ++modelSelectionRevision
    try {
      const snapshot = await request<ModelSelectionSnapshot>('/model-selection')
      if (generation === epoch && revision === modelSelectionRevision) modelSelection.value = snapshot || null
    } catch (error) {
      if (generation !== epoch || revision !== modelSelectionRevision) return
      if (error instanceof APIError && error.status === 501) modelSelection.value = null
      else if (!isAbort(error)) notify(errorMessage(error))
    }
  }
  async function updateModelSelection(selection: ModelSelection, revision: string) {
    try {
      const snapshot = await command<ModelSelectionSnapshot>('/model-selection', 'PUT', { selection, revision })
      modelSelectionRevision++
      modelSelection.value = snapshot
    } catch (error) {
      if (error instanceof APIError && error.status === 409) await refreshModelSelection()
      throw error
    }
  }
  async function connect() {
    if (lifecycle) return
    lifecycle = new AbortController()
    const signal = lifecycle.signal
    let attempts = 0
    while (!signal.aborted) {
      try {
        const snapshot = await request<Snapshot>('/state', { signal })
        bootstrap(snapshot)
        await subscribe(snapshot.cursor, signal, receive, () => {
          connection.value = 'online'
          connectionError.value = ''
          attempts = 0
        })
      } catch (error) {
        if (signal.aborted) break
        connectionError.value = errorMessage(error)
        if (error instanceof APIError && error.status === 409) {
          connection.value = 'reconnecting'
          continue
        }
      }
      if (signal.aborted) break
      connection.value = 'reconnecting'
      const delay = Math.min(1000 * 2 ** attempts++, 15000)
      await new Promise<void>(resolve => {
        const done = () => { clearTimeout(timer); signal.removeEventListener('abort', done); resolve() }
        const timer = setTimeout(done, delay)
        signal.addEventListener('abort', done, { once: true })
      })
    }
  }
  function disconnect() {
    lifecycle?.abort()
    lifecycle = undefined
    for (const controller of historyRequests.values()) controller.abort()
  }
  async function createSession(title: string, workspace: string) {
    const session = await command<Session>('/sessions', 'POST', { title, workspace })
    sessionRevision++
    sessions.value = [...sessions.value.filter(item => item.id !== session.id), session]
    return session
  }
  async function loadFollowups(id: string) {
    const generation = epoch
    const items = await request<Followup[]>('/sessions/' + segment(id) + '/followups')
    if (generation === epoch) followups.value[id] = items || []
  }
  async function createFollowup(id: string, anchor: FollowupAnchor) {
    const item = await command<Followup>('/sessions/' + segment(id) + '/followups', 'POST', anchor)
    followups.value[id] = [...(followups.value[id] || []).filter(note => note.id !== item.id), item]
    histories.value[item.id] = []
    return item
  }
  async function deleteFollowup(item: Followup) {
    await command('/followups/' + segment(item.id), 'DELETE')
    followups.value[item.sourceSessionId] = (followups.value[item.sourceSessionId] || []).filter(note => note.id !== item.id)
    delete histories.value[item.id]
  }
  async function assignWorkspace(id: string, workspace: string) {
    const session = await command<Session>('/sessions/' + segment(id) + '/workspace', 'POST', { workspace })
    sessionRevision++
    sessions.value = [...sessions.value.filter(item => item.id !== session.id), session]
    return session
  }
  const pickWorkspace = (initialPath: string) => command<WorkspaceSelection>('/workspace/select', 'POST', { initialPath })
  async function mutateSession(id: string, action: 'rename' | 'archive' | 'restore' | 'delete' | 'fork', title?: string) {
    const path = '/sessions/' + segment(id)
    const item = await command<Session | undefined>(
      path + (['archive', 'restore', 'fork'].includes(action) ? '/' + action : ''),
      action === 'rename' ? 'PATCH' : action === 'delete' ? 'DELETE' : 'POST',
      action === 'rename' || action === 'fork' ? { title: title || '' } : undefined,
    )
    sessionRevision++
    if (action === 'delete') {
      sessions.value = sessions.value.filter(session => session.id !== id)
      delete histories.value[id]
    } else if (item) {
      sessions.value = [...sessions.value.filter(session => session.id !== item.id), item]
    }
    return item
  }
  async function send(sessionId: string, input: string, attachments: Attachment[]) {
    const generation = epoch
    const result = await command<{ id: string }>('/turns', 'POST', { sessionId, input, attachments })
    if (generation !== epoch) return
    const turn = turns.value[result.id]
    if (!turn) turns.value[result.id] = { id: result.id, sessionId, revision: 0, output: '', reasoning: '', status: 'running' }
    if (!turn?.reconciled) optimistic.value[result.id] = {
      sessionId, message: { role: 'user', content: [
        ...(input ? [{ kind: 'text', text: input }] : []),
        ...attachments.map(attachment => ({ kind: attachment.kind, name: attachment.name, mimeType: attachment.mimeType, source: { kind: 'asset', assetId: attachment.assetId } })),
      ] },
    }
  }
  async function stop(turn: LiveTurn) {
    turn.stopping = true
    try { await command('/turns/' + segment(turn.id), 'DELETE') }
    catch (error) { turn.stopping = false; throw error }
  }
  async function respond(id: string, values: Record<string, unknown>) {
    const invocationId = interactions.value[id]?.scope?.operation?.invocationId
    try {
      await command('/interactions/' + segment(id) + '/response', 'POST', { values })
      delete interactions.value[id]
      delete interactionDrafts.value[id]
    } catch (error) {
      if (error instanceof APIError && error.status === 409) {
        delete interactions.value[id]
        delete interactionDrafts.value[id]
      }
      throw error
    }
    if (invocationId && !Object.values(interactions.value).some(item => item.scope?.operation?.invocationId === invocationId)) {
      delete suspendedOperations.value[invocationId]
    }
  }
  async function refreshOperationState(id: string) {
    const generation = epoch
    const snapshot = await request<Snapshot>('/state')
    if (generation !== epoch) return
    const invocation = snapshot.operationInvocations.find(item => item.id === id)
    if (!invocation) return false
    const current = operationInvocations.value[id]
    if (!current || current.status === 'running' || invocation.status !== 'running') {
      operationInvocations.value[id] = invocation
    }
    if (snapshot.cursor >= cursor.value || !current) {
      for (const item of Object.values(interactions.value)) {
        if (item.scope?.operation?.invocationId === id) delete interactions.value[item.id]
      }
      for (const item of snapshot.interactions) {
        if (item.scope?.operation?.invocationId === id && !dismissedOperations.has(id)) interactions.value[item.id] = item
      }
      for (const item of Object.values(interactionStates.value)) {
        if (item.scope?.operation?.invocationId === id) delete interactionStates.value[item.id]
      }
      for (const item of snapshot.interactionStates) {
        if (item.scope?.operation?.invocationId === id && !dismissedOperations.has(id)) interactionStates.value[item.id] = item
      }
    }
    return true
  }
  async function invoke(operationId: string, input: string, sessionId: string) {
    // The path parameter is the operation's internal ID; same-name operations
    // from different Plugins stay independently addressable.
    // Preserve the original JSON text, including integers outside JS precision.
    return request<{ id: string }>('/operations/' + segment(operationId), {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: '{"sessionId":' + JSON.stringify(sessionId) + ',"input":' + input + '}',
    })
  }
  function dismissOperationInteractions(id: string) {
    dismissedOperations.add(id)
    clearOperationDrafts(id)
    for (const item of Object.values(interactions.value)) {
      if (item.scope?.operation?.invocationId === id) delete interactions.value[item.id]
    }
    for (const item of Object.values(interactionStates.value)) {
      if (item.scope?.operation?.invocationId === id) delete interactionStates.value[item.id]
    }
  }
  async function cancelOperation(id: string) {
    await command('/operation-invocations/' + segment(id), 'DELETE')
    dismissOperationInteractions(id)
  }
  function clearOperationDrafts(id: string) {
    delete suspendedOperations.value[id]
    for (const item of Object.values(interactions.value)) {
      if (item.scope?.operation?.invocationId === id) delete interactionDrafts.value[item.id]
    }
  }
  function suspendOperation(id: string) { suspendedOperations.value[id] = true }
  function resumeOperation(id: string) { delete suspendedOperations.value[id] }
  return {
    sessions, followups, orderedSessions, capabilities, assets, defaultWorkspace, turns, interactions, interactionDrafts, interactionStates,
    operations, modelSelection, operationInvocations, histories, historyLoading, historyErrors, optimistic,
    traces, notices, connection, connectionError, activeSession, cursor, pendingCount, pendingOperationRequests,
    notify, running, loadHistory, refreshSessions, refreshModelSelection, updateModelSelection, refreshOperationState, bootstrap, receive, connect, disconnect,
    createSession, loadFollowups, createFollowup, deleteFollowup, assignWorkspace, pickWorkspace, mutateSession, send, stop, respond, invoke, cancelOperation, dismissOperationInteractions, suspendOperation, resumeOperation,
  }
})

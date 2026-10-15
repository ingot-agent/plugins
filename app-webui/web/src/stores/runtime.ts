import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { APIError, command, errorMessage, isAbort, request, segment } from '../api'
import { subscribe } from '../sse'
import { acceptTurn, bootstrapTurns, indexById, reduceOperation, reduceTurn } from '../state'
import { equalInteractionValue } from '../forms'
import { parseContextUsage } from '../usage'
import type { Attachment, ContextUsage, Followup, FollowupAnchor, Interaction, InteractionState, LiveTurn, Message, ModelSelection, ModelSelectionSnapshot, Notice, Operation, OperationInvocation, Part, Session, Snapshot, TraceEvent, Turn, TurnQueue, WebEvent, WorkspaceSelection } from '../protocol'

export const useRuntime = defineStore('runtime', () => {
  const sessions = ref<Session[]>([])
  const totalTokenBySession = ref<Record<string, number>>({})
  const contextBySession = ref<Record<string, ContextUsage>>({})
  const followups = ref<Record<string, Followup[]>>({})
  const capabilities = ref({ sendMessage: false })
  const assets = ref({ available: false, maxBytes: 0 })
  const files = ref({ available: false, maxBytes: 0 })
  const defaultWorkspace = ref('')
  const turns = ref<Record<string, LiveTurn>>({})
  const turnQueues = ref<Record<string, TurnQueue>>({})
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
  const deletedSessions = new Set<string>()
  const historyRequests = new Map<string, AbortController>()
  const uncertainSubmissions = new Map<string, { signature: string; requestId: string }>()
  let runtimeId: string | undefined
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
  function queued(sessionId: string) {
    return Object.values(turns.value).filter(turn => turn.sessionId === sessionId && turn.status === 'queued')
      .sort((left, right) => left.sequence - right.sequence)
  }
  function acceptQueue(queue: TurnQueue) {
    if (deletedSessions.has(queue.sessionId)) return
    const previous = turnQueues.value[queue.sessionId]
    if (!previous || queue.revision > previous.revision) turnQueues.value[queue.sessionId] = queue
  }
  function inputMessage(input: string, attachments: Attachment[]): Message {
    return { role: 'user', content: [
      ...(input ? [{ kind: 'text', text: input }] : []),
      ...attachments.map((attachment): Part => {
        const image = attachment.kind === 'image' && Boolean(attachment.assetId)
        return { kind: image ? 'image' : 'file', name: attachment.name, mimeType: attachment.mimeType,
          source: image ? { kind: 'asset', assetId: attachment.assetId } : { kind: 'local', path: attachment.path } }
      }),
    ] }
  }
  function projectInput(turn: LiveTurn) {
    if (!turn.started || turn.status === 'queued' || turn.reconciled || deletedSessions.has(turn.sessionId)) return
    optimistic.value[turn.id] = { sessionId: turn.sessionId, message: inputMessage(turn.input, turn.attachments || []) }
  }
  function mergeSessionTotal(id: string, total: unknown) {
    if (!id || deletedSessions.has(id) || typeof total !== 'number' || !Number.isSafeInteger(total) || total < 0) return
    totalTokenBySession.value[id] = Math.max(totalTokenBySession.value[id] ?? 0, total)
  }
  function mergeSession(item: Session) {
    mergeSessionTotal(item.id, item.totalToken)
    return item
  }
  function acceptState(item: InteractionState) {
    if (item.name?.startsWith('context-compact.session-context/')) {
      const context = parseContextUsage(item)
      if (!context || deletedSessions.has(context.sessionId)) return
      contextBySession.value[context.sessionId] = context
      return
    }
    if (item.name?.startsWith('model-runtime.session-usage/')) {
      const id = item.scope?.agent?.sessionId
      if (!id || deletedSessions.has(id) || item.scope?.operation || item.id !== item.name || item.name !== 'model-runtime.session-usage/' + id) return
      const identity = item.values?.filter(value => value.name === 'sessionId')
      const total = item.values?.filter(value => value.name === 'totalToken')
      if (identity?.length !== 1 || identity[0]?.value !== id || total?.length !== 1) return
      mergeSessionTotal(id, total[0]?.value)
      return
    }
    interactionStates.value[item.id] = item
  }
  function forgetSession(id: string) {
    deletedSessions.add(id)
    delete totalTokenBySession.value[id]
    delete contextBySession.value[id]
    delete traces.value[id]
    delete interactionStates.value['model-runtime.session-usage/' + id]
    historyRequests.get(id)?.abort()
    delete turnQueues.value[id]
    uncertainSubmissions.delete(id)
    for (const turn of Object.values(turns.value)) {
      if (turn.sessionId === id) { delete turns.value[turn.id]; delete optimistic.value[turn.id] }
    }
  }
  async function loadSession(id: string) {
    const generation = epoch
    try {
      const item = await request<Session>('/sessions/' + segment(id))
      if (generation === epoch && !deletedSessions.has(id)) mergeSession(item)
    } catch (error) {
      if (generation === epoch && !isAbort(error) && !(error instanceof APIError && error.status === 404)) notify(errorMessage(error))
    }
  }
  async function loadHistory(id: string) {
    if (!id || deletedSessions.has(id)) return
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
        if (turn.sessionId === id && turn.started !== false && turn.persisted !== false && turn.status !== 'running' && turn.status !== 'queued' && !turn.reconciled && (turn.historyEnd === undefined || turn.historyEnd <= (messages?.length || 0))) {
          turn.reconciled = true
          turn.historyEnd ??= messages?.length || 0
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
      if (generation === epoch && revision === sessionRevision) sessions.value = (items || []).filter(item => !deletedSessions.has(item.id)).map(mergeSession)
    } catch (error) { if (!isAbort(error)) notify(errorMessage(error)) }
  }
  function bootstrap(snapshot: Snapshot) {
    if (runtimeId !== snapshot.runtimeId) uncertainSubmissions.clear()
    runtimeId = snapshot.runtimeId
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
    sessions.value = (snapshot.sessions || []).filter(item => !deletedSessions.has(item.id)).map(mergeSession)
    capabilities.value = snapshot.agent.capabilities
    assets.value = snapshot.assets || { available: false, maxBytes: 0 }
    files.value = snapshot.files || { available: false, maxBytes: 0 }
    defaultWorkspace.value = snapshot.workspace?.defaultPath || ''
    turns.value = bootstrapTurns(snapshot)
    turnQueues.value = Object.fromEntries((snapshot.turnQueues || []).filter(queue => !deletedSessions.has(queue.sessionId)).map(queue => [queue.sessionId, queue]))
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
    interactionStates.value = {}
    contextBySession.value = {}
    for (const item of snapshot.interactionStates || []) acceptState(item)
    operations.value = snapshot.operations || []
    operationInvocations.value = indexById(snapshot.operationInvocations)
    // Process-local identifiers may be reused after a server restart.
    traces.value = {}
    optimistic.value = {}
    for (const turn of Object.values(turns.value)) projectInput(turn)
    if (activeSession.value) {
      void loadSession(activeSession.value)
      void loadHistory(activeSession.value)
      void loadFollowups(activeSession.value).catch(error => notify(errorMessage(error)))
    }
  }
  function acceptRuntime(inputs: Turn[], queues: TurnQueue[]) {
    const finishedSessions = new Set<string>()
    for (const snapshot of inputs) {
      if (deletedSessions.has(snapshot.sessionId)) continue
      const previous = turns.value[snapshot.id]
      acceptTurn(turns.value, snapshot)
      const current = turns.value[snapshot.id]!
      if (current !== previous && current.status !== 'queued' && current.status !== 'running' &&
        (!previous || previous.status === 'queued' || previous.status === 'running')) finishedSessions.add(current.sessionId)
    }
    for (const queue of queues) acceptQueue(queue)
    // Interaction delivery may precede the first authoritative submission
    // snapshot. Associate pending requests once that lifecycle is known.
    for (const interaction of Object.values(interactions.value)) {
      reduceTurn(turns.value, { type: 'interaction.requested', scope: interaction.scope, data: interaction })
    }
    for (const turn of Object.values(turns.value)) projectInput(turn)
    for (const sid of finishedSessions) {
      if (sid === activeSession.value || histories.value[sid]) { void loadHistory(sid); void loadSession(sid) }
    }
    if (finishedSessions.size) void refreshSessions()
  }
  function receive(id: number, event: WebEvent) {
    if (id <= cursor.value) return
    cursor.value = id
    const data = event.data || {}
    event.data = data
    if (event.scope?.agent?.sessionId && deletedSessions.has(event.scope.agent.sessionId)) return
    if (event.type === 'agent.runtime.updated') acceptRuntime(data.turns, data.turnQueues)
    else reduceTurn(turns.value, event)
    if (event.type === 'model.selection.updated') {
      modelSelectionRevision++
      modelSelection.value = data as ModelSelectionSnapshot
    } else if (/^agent\.(turn|round|model|tool)\./.test(event.type)) {
      const sid = event.scope?.agent?.sessionId
      if (sid && !deletedSessions.has(sid)) traces.value[sid] = [...(traces.value[sid] || []), { ...event, cursor: id }].slice(-500)
    } else if (event.type.startsWith('session.')) {
      sessionRevision++
      if (event.type === 'session.deleted') {
        forgetSession(data.id)
        sessions.value = sessions.value.filter(session => session.id !== data.id)
        delete histories.value[data.id]
        delete followups.value[data.id]
        for (const id of Object.keys(followups.value)) followups.value[id] = followups.value[id].filter(note => note.id !== data.id)
        historyRequests.get(data.id)?.abort()
      } else {
        const item = data as Session
        if (deletedSessions.has(item.id)) return
        mergeSession(item)
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
      acceptState(data as InteractionState)
    } else if (event.type === 'interaction.state.clear') {
      delete interactionStates.value[data.id]
      if (typeof data.id === 'string' && data.id.startsWith('context-compact.session-context/')) delete contextBySession.value[data.id.slice('context-compact.session-context/'.length)]
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
    const owner = new AbortController()
    lifecycle = owner
    connection.value = 'connecting'
    const signal = owner.signal
    let stream: AbortController | undefined
    let checking = false
    let attempts = 0

    async function readState(signal: AbortSignal) {
      const timeout = new AbortController()
      const timer = setTimeout(() => timeout.abort(new Error('State request timed out')), 10000)
      try {
        return await request<Snapshot>('/state', { signal: AbortSignal.any([signal, timeout.signal]) })
      } finally { clearTimeout(timer) }
    }
    async function checkState() {
      const currentStream = stream
      if (checking || !currentStream || currentStream.signal.aborted || signal.aborted || connection.value !== 'online') return
      checking = true
      const generation = epoch
      const checkSignal = AbortSignal.any([signal, currentStream.signal])
      try {
        const snapshot = await readState(checkSignal)
        if (checkSignal.aborted || generation !== epoch) return
        if (snapshot.runtimeId !== runtimeId) {
          currentStream.abort()
          return
        }
        // Merge by revision without advancing the SSE cursor: delayed events
        // can still contain approvals or tool activity absent from this view.
        acceptRuntime(snapshot.turns, snapshot.turnQueues || [])
        if (snapshot.cursor > cursor.value) currentStream.abort()
      } catch (error) {
        if (!checkSignal.aborted) currentStream.abort(error)
      } finally { checking = false }
    }
    const checkPending = setInterval(() => {
      if (Object.values(turns.value).some(turn => turn.status === 'running' || turn.status === 'queued')) void checkState()
    }, 10000)
    const resume = () => { if (document.visibilityState === 'visible') void checkState() }
    window.addEventListener('focus', resume)
    window.addEventListener('online', resume)
    document.addEventListener('visibilitychange', resume)
    try {
      while (!signal.aborted) {
        stream = new AbortController()
        const streamSignal = AbortSignal.any([signal, stream.signal])
        try {
          const snapshot = await readState(streamSignal)
          if (streamSignal.aborted) continue
          bootstrap(snapshot)
          await subscribe(snapshot.cursor, streamSignal, (id, event) => {
            if (!streamSignal.aborted) receive(id, event)
          }, () => {
            if (streamSignal.aborted) return
            connection.value = 'online'
            connectionError.value = ''
            attempts = 0
          })
        } catch (error) {
          if (signal.aborted) break
          connectionError.value = errorMessage(error)
          if (error instanceof APIError && error.status === 409) stream.abort()
        }
        if (signal.aborted) break
        connection.value = 'reconnecting'
        if (stream.signal.aborted) continue
        const delay = Math.min(1000 * 2 ** attempts++, 15000)
        await new Promise<void>(resolve => {
          const done = () => { clearTimeout(timer); signal.removeEventListener('abort', done); resolve() }
          const timer = setTimeout(done, delay)
          signal.addEventListener('abort', done, { once: true })
        })
      }
    } finally {
      stream?.abort()
      clearInterval(checkPending)
      window.removeEventListener('focus', resume)
      window.removeEventListener('online', resume)
      document.removeEventListener('visibilitychange', resume)
      if (lifecycle === owner) lifecycle = undefined
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
    mergeSession(session)
    sessions.value = [...sessions.value.filter(item => item.id !== session.id), session]
    return session
  }
  async function loadFollowups(id: string) {
    const generation = epoch
    const items = await request<Followup[]>('/sessions/' + segment(id) + '/followups')
    if (generation === epoch && !deletedSessions.has(id)) followups.value[id] = (items || []).filter(item => !deletedSessions.has(item.id))
  }
  async function createFollowup(id: string, anchor: FollowupAnchor) {
    const item = await command<Followup>('/sessions/' + segment(id) + '/followups', 'POST', anchor)
    followups.value[id] = [...(followups.value[id] || []).filter(note => note.id !== item.id), item]
    histories.value[item.id] = []
    mergeSessionTotal(item.id, 0)
    return item
  }
  async function deleteFollowup(item: Followup) {
    await command('/followups/' + segment(item.id), 'DELETE')
    forgetSession(item.id)
    followups.value[item.sourceSessionId] = (followups.value[item.sourceSessionId] || []).filter(note => note.id !== item.id)
    delete histories.value[item.id]
  }
  async function assignWorkspace(id: string, workspace: string) {
    const session = await command<Session>('/sessions/' + segment(id) + '/workspace', 'POST', { workspace })
    sessionRevision++
    mergeSession(session)
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
      forgetSession(id)
      sessions.value = sessions.value.filter(session => session.id !== id)
      delete histories.value[id]
    } else if (item) {
      mergeSession(item)
      sessions.value = [...sessions.value.filter(session => session.id !== item.id), item]
    }
    return item
  }
  async function send(sessionId: string, input: string, attachments: Attachment[]) {
    const generation = epoch
    const server = runtimeId
    const owned = attachments.map(attachment => ({ ...attachment }))
    const signature = JSON.stringify({ input, attachments: owned })
    let submission = uncertainSubmissions.get(sessionId)
    if (!submission || submission.signature !== signature) {
      submission = { signature, requestId: crypto.randomUUID() }
      uncertainSubmissions.set(sessionId, submission)
    }
    let result: Turn
    try {
      result = await command<Turn>('/turns', 'POST', { sessionId, input, attachments: owned, requestId: submission.requestId })
    } catch (error) {
      const accepted = Object.values(turns.value).find(turn => turn.requestId === submission.requestId)
      if (accepted) { uncertainSubmissions.delete(sessionId); return }
      if (error instanceof APIError && error.status < 500) uncertainSubmissions.delete(sessionId)
      throw error
    }
    if (generation !== epoch && runtimeId !== server) throw new Error('The server restarted before the submission could be confirmed.')
    uncertainSubmissions.delete(sessionId)
    if (deletedSessions.has(sessionId)) return
    acceptTurn(turns.value, result)
    const turn = turns.value[result.id]!
    projectInput(turn)
    if (turn.status !== 'queued' && turn.status !== 'running') {
      void loadHistory(sessionId)
      return
    }
  }
  async function setQueuePaused(sessionId: string, paused: boolean) {
    const generation = epoch
    const queue = await command<TurnQueue>('/sessions/' + segment(sessionId) + '/turn-queue', 'PATCH', { paused })
    if (generation === epoch) acceptQueue(queue)
  }
  async function removeQueued(turn: LiveTurn) {
    await command('/turns/' + segment(turn.id) + '?queued=true', 'DELETE')
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
    sessions, totalTokenBySession, contextBySession, followups, orderedSessions, capabilities, assets, files, defaultWorkspace, turns, turnQueues, interactions, interactionDrafts, interactionStates,
    operations, modelSelection, operationInvocations, histories, historyLoading, historyErrors, optimistic,
    traces, notices, connection, connectionError, activeSession, cursor, pendingCount, pendingOperationRequests,
    notify, running, queued, setQueuePaused, removeQueued, loadSession, loadHistory, refreshSessions, refreshModelSelection, updateModelSelection, refreshOperationState, bootstrap, receive, connect, disconnect,
    createSession, loadFollowups, createFollowup, deleteFollowup, assignWorkspace, pickWorkspace, mutateSession, send, stop, respond, invoke, cancelOperation, dismissOperationInteractions, suspendOperation, resumeOperation,
  }
})

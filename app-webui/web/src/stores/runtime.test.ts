import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { command, request } from '../api'
import type { Interaction, InteractionState, Message, ModelSelectionSnapshot, OperationInvocation, Session, Snapshot } from '../protocol'
import { useRuntime } from './runtime'

vi.mock('../api', async importOriginal => ({
  ...await importOriginal<typeof import('../api')>(),
  request: vi.fn(), command: vi.fn(),
}))

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
const session = (title: string): Session => ({ id: 's', title, createdAt: '2026-01-01', updatedAt: '2026-01-01', totalToken: 0 })
const snapshot = (): Snapshot => ({
  cursor: 4, agent: { capabilities: { run: true, stream: true } },
  workspace: { defaultPath: '/state/workspace' },
  sessions: [session('Initial')], turns: [], interactions: [], interactionStates: [], operations: [], operationInvocations: [],
})
const selection = (revision: string, model: string): ModelSelectionSnapshot => ({
  revision, configured: true, current: { provider: 'provider', model, reasoningEffort: 'low' },
  providers: [{ name: 'provider', models: [{ name: model, reasoningEfforts: ['low'] }] }],
})

beforeEach(() => { setActivePinia(createPinia()); vi.resetAllMocks() })

const usageState = (id: string, totalToken: unknown): InteractionState => ({
  id: 'model-runtime.session-usage/' + id, name: 'model-runtime.session-usage/' + id,
  scope: { agent: { sessionId: id } }, values: [{ name: 'sessionId', value: id }, { name: 'totalToken', value: totalToken }],
})

describe('session token totals', () => {
  it('merges persisted bootstrap totals and dedicated states without generic panels', () => {
    const runtime = useRuntime()
    const state = snapshot()
    state.sessions[0].totalToken = 10
    state.interactionStates = [usageState('s', 20), usageState('followup', 7), { id: 'normal', name: 'normal', values: [] }]
    runtime.bootstrap(state)
    expect(runtime.totalTokenBySession).toEqual({ s: 20, followup: 7 })
    expect(Object.keys(runtime.interactionStates)).toEqual(['normal'])
    runtime.receive(5, { type: 'interaction.state.set', data: usageState('s', 15) })
    runtime.receive(6, { type: 'interaction.state.set', data: usageState('s', 20) })
    runtime.receive(7, { type: 'interaction.state.set', data: usageState('child', 3) })
    expect(runtime.totalTokenBySession).toEqual({ s: 20, followup: 7, child: 3 })
  })

  it('keeps newer Set ahead of slow session queries and restores hidden sessions', async () => {
    const runtime = useRuntime()
    const stale = deferred<Session[]>()
    vi.mocked(request).mockReturnValueOnce(stale.promise)
    const pending = runtime.refreshSessions()
    runtime.receive(1, { type: 'interaction.state.set', data: usageState('s', 30) })
    stale.resolve([{ ...session('stale'), totalToken: 10 }])
    await pending
    expect(runtime.totalTokenBySession.s).toBe(30)
    vi.mocked(request).mockResolvedValueOnce({ ...session('note'), id: 'followup', totalToken: 11 })
    await runtime.loadSession('followup')
    expect(runtime.totalTokenBySession.followup).toBe(11)
    expect(runtime.sessions).toHaveLength(1)
  })

  it('rejects forged scope, identities and invalid totals', () => {
    const runtime = useRuntime()
    const mismatch = usageState('s', 10)
    mismatch.scope = { agent: { sessionId: 'other' } }
    const forged = usageState('s', 10)
    forged.values[0].value = 'other'
    const operation = usageState('s', 10)
    operation.scope!.operation = { invocationId: 'op' }
    for (const [i, state] of [mismatch, forged, operation, ...[-1, NaN, Infinity, 1.5, '10', Number.MAX_SAFE_INTEGER + 1].map(total => usageState('s', total))].entries()) {
      runtime.receive(i + 1, { type: 'interaction.state.set', data: state })
    }
    expect(runtime.totalTokenBySession).toEqual({})
    expect(runtime.interactionStates).toEqual({})
  })

  it('does not resurrect deleted totals from pending queries, late Set or reconnect', async () => {
    const runtime = useRuntime()
    runtime.receive(1, { type: 'interaction.state.set', data: usageState('s', 10) })
    const stale = deferred<Session>()
    vi.mocked(request).mockReturnValueOnce(stale.promise)
    const pending = runtime.loadSession('s')
    runtime.receive(2, { type: 'session.deleted', data: { id: 's' } })
    runtime.receive(3, { type: 'interaction.state.set', data: usageState('s', 20) })
    stale.resolve({ ...session('deleted'), totalToken: 30 })
    await pending
    const state = snapshot()
    state.interactionStates = [usageState('s', 40)]
    runtime.bootstrap(state)
    expect(runtime.totalTokenBySession.s).toBeUndefined()
    expect(runtime.sessions).toHaveLength(0)
  })

  it('starts ordinary forks at their own persisted zero', async () => {
    const runtime = useRuntime()
    runtime.receive(1, { type: 'interaction.state.set', data: usageState('s', 189) })
    vi.mocked(command).mockResolvedValueOnce({ ...session('fork'), id: 'fork', totalToken: 0 })
    await runtime.mutateSession('s', 'fork', 'fork')
    expect(runtime.totalTokenBySession).toEqual({ s: 189, fork: 0 })
  })
})

const contextState = (id: string, inputTokens: number, turnId = 'turn'): InteractionState => ({
  id: 'context-compact.session-context/' + id, name: 'context-compact.session-context/' + id,
  scope: { agent: { sessionId: id } }, values: Object.entries({ sessionId: id, inputTokens, turnId, roundIndex: 0, accuracy: 'estimate', source: 'estimator', provider: 'p', model: 'm' }).map(([name, value]) => ({ name, value })),
})

describe('session context estimates', () => {
  it('replaces shrinking estimates and keeps Sessions independent', () => {
    const runtime = useRuntime()
    runtime.receive(1, { type: 'interaction.state.set', data: contextState('s', 100) })
    runtime.receive(2, { type: 'interaction.state.set', data: contextState('child', 30) })
    runtime.receive(3, { type: 'interaction.state.set', data: contextState('s', 20) })
    runtime.receive(3, { type: 'interaction.state.set', data: contextState('s', 99) })
    expect(runtime.contextBySession.s.inputTokens).toBe(20)
    expect(runtime.contextBySession.child.inputTokens).toBe(30)
    expect(runtime.interactionStates).toEqual({})
  })

  it('restores the latest Session context on bootstrap', () => {
    const runtime = useRuntime()
    runtime.receive(1, { type: 'interaction.state.set', data: contextState('s', 100, 'old') })
    const state = snapshot()
    state.interactionStates = [contextState('s', 40, 'latest')]
    runtime.bootstrap(state)
    expect(runtime.contextBySession.s.inputTokens).toBe(40)
    expect(runtime.contextBySession.s.turnId).toBe('latest')
    runtime.receive(5, { type: 'interaction.state.clear', data: { id: 'context-compact.session-context/s' } })
    expect(runtime.contextBySession.s).toBeUndefined()
  })

  it('clears deleted context and rejects late updates', () => {
    const runtime = useRuntime()
    runtime.receive(1, { type: 'interaction.state.set', data: contextState('s', 100) })
    runtime.receive(2, { type: 'session.deleted', data: { id: 's' } })
    runtime.receive(3, { type: 'interaction.state.set', data: contextState('s', 200) })
    expect(runtime.contextBySession.s).toBeUndefined()
  })
})

describe('runtime request and event ordering', () => {
  it('keeps a newer model selection event ahead of a pending refresh', async () => {
    const runtime = useRuntime()
    const stale = deferred<ModelSelectionSnapshot>()
    vi.mocked(request).mockReturnValueOnce(stale.promise)
    const pending = runtime.refreshModelSelection()
    runtime.receive(1, { type: 'model.selection.updated', data: selection('new', 'model-b') })
    stale.resolve(selection('old', 'model-a'))
    await pending
    expect(runtime.modelSelection?.current.model).toBe('model-b')
  })

  it('submits the selected model and effort with its revision', async () => {
    const runtime = useRuntime()
    runtime.modelSelection = selection('old', 'model-a')
    vi.mocked(command).mockResolvedValueOnce(selection('new', 'model-b'))
    await runtime.updateModelSelection({ provider: 'provider', model: 'model-b', reasoningEffort: 'low' }, 'old')
    expect(command).toHaveBeenCalledWith('/model-selection', 'PUT', {
      revision: 'old', selection: { provider: 'provider', model: 'model-b', reasoningEffort: 'low' },
    })
    expect(runtime.modelSelection?.revision).toBe('new')
  })

  it('does not let a slow session refresh undo a newer SSE mutation', async () => {
    const runtime = useRuntime()
    runtime.bootstrap(snapshot())
    const stale = deferred<Session[]>()
    vi.mocked(request).mockReturnValueOnce(stale.promise)
    const pending = runtime.refreshSessions()
    runtime.receive(5, { type: 'session.updated', data: session('Renamed') })
    stale.resolve([session('Initial')])
    await pending
    expect(runtime.sessions[0].title).toBe('Renamed')
  })

  it('keeps the newest of overlapping history requests', async () => {
    const runtime = useRuntime()
    const first = deferred<Message[]>()
    vi.mocked(request).mockReturnValueOnce(first.promise).mockResolvedValueOnce([{ role: 'assistant', content: [{ kind: 'text', text: 'new' }] }])
    const pending = runtime.loadHistory('s')
    await runtime.loadHistory('s')
    first.resolve([{ role: 'assistant', content: [{ kind: 'text', text: 'old' }] }])
    await pending
    expect(runtime.histories.s[0].content[0].text).toBe('new')
    expect(runtime.historyLoading.s).toBe(false)
  })

  it('does not carry process-local state or stale requests across bootstrap', async () => {
    const runtime = useRuntime()
    runtime.bootstrap(snapshot())
    runtime.receive(5, { type: 'agent.tool.started', scope: { agent: { sessionId: 's', turnId: 'sdk' } }, data: {} })
    runtime.histories.s = [{ role: 'user', content: [] }]
    runtime.optimistic.web = { sessionId: 's', message: { role: 'user', content: [] } }
    const stale = deferred<Message[]>()
    vi.mocked(request).mockReturnValueOnce(stale.promise)
    const pending = runtime.loadHistory('s')
    runtime.bootstrap({ ...snapshot(), cursor: 0 })
    stale.resolve([{ role: 'assistant', content: [] }])
    await pending
    expect(runtime.cursor).toBe(0)
    expect(runtime.traces).toEqual({})
    expect(runtime.optimistic).toEqual({})
    expect(runtime.histories).toEqual({})
    expect(runtime.historyLoading).toEqual({})
  })

  it('never automatically retries an uncertain turn submission', async () => {
    const runtime = useRuntime()
    vi.mocked(command).mockRejectedValueOnce(new TypeError('Connection lost'))
    await expect(runtime.send('s', 'hello', [])).rejects.toThrow('Connection lost')
    expect(command).toHaveBeenCalledTimes(1)
    expect(runtime.turns).toEqual({})
  })

  it('assigns a workspace once and replaces the unbound session projection', async () => {
    const runtime = useRuntime()
    runtime.bootstrap(snapshot())
    const bound = { ...session('Initial'), workspace: '/repo/project' }
    vi.mocked(command).mockResolvedValueOnce(bound)
    await runtime.assignWorkspace('s', '/repo/project')
    expect(command).toHaveBeenCalledWith('/sessions/s/workspace', 'POST', { workspace: '/repo/project' })
    expect(runtime.sessions).toEqual([bound])
  })

  it('loads the default workspace and requests the native picker', async () => {
    const runtime = useRuntime()
    runtime.bootstrap(snapshot())
    vi.mocked(command).mockResolvedValueOnce({ path: '/repo/project' })
    await expect(runtime.pickWorkspace(runtime.defaultWorkspace)).resolves.toEqual({ path: '/repo/project' })
    expect(runtime.defaultWorkspace).toBe('/state/workspace')
    expect(command).toHaveBeenCalledWith('/workspace/select', 'POST', { initialPath: '/state/workspace' })
  })

  it('retains ordered tool cards after detailed trace retention rolls over', () => {
    const runtime = useRuntime()
    runtime.bootstrap(snapshot())
    runtime.receive(5, { type: 'agent.invocation.started', data: { id: 'web', sessionId: 's', revision: 0, reasoning: '', output: '' } })
    const scope = { agent: { sessionId: 's', turnId: 'sdk', toolCallId: 'tool' } }
    runtime.receive(6, { type: 'agent.tool.started', scope, data: { call: { id: 'tool', name: 'inspect', arguments: {} } } })
    for (let id = 7; id < 550; id++) runtime.receive(id, { type: 'agent.model.progress', scope, data: {} })
    runtime.receive(550, { type: 'agent.tool.finished', scope, data: { status: 'succeeded', result: { content: [{ kind: 'text', text: 'Done' }] } } })
    runtime.receive(551, { type: 'agent.reasoning.delta', data: { invocationId: 'web', revision: 1, text: 'Next thought' } })
    runtime.receive(551, { type: 'agent.reasoning.delta', data: { invocationId: 'web', revision: 1, text: 'Duplicate' } })
    expect(runtime.traces.s).toHaveLength(500)
    expect(runtime.turns.web.blocks).toMatchObject([
      { kind: 'tool', status: 'succeeded', content: [{ text: 'Done' }] },
      { kind: 'reasoning', text: 'Next thought' },
    ])
  })

  it('keeps a completed invocation anchored before subsequent history', async () => {
    const runtime = useRuntime()
    runtime.turns.web = { id: 'web', sessionId: 's', revision: 1, reasoning: '', output: 'Partial', status: 'canceled' }
    const first: Message[] = [{ role: 'user', content: [{ kind: 'text', text: 'First request' }] }]
    vi.mocked(request).mockResolvedValueOnce(first)
    await runtime.loadHistory('s')
    expect(runtime.turns.web.historyEnd).toBe(1)
    vi.mocked(request).mockResolvedValueOnce([...first, { role: 'user', content: [{ kind: 'text', text: 'Next request' }] }])
    await runtime.loadHistory('s')
    expect(runtime.turns.web.historyEnd).toBe(1)
  })

  it('keeps large integer operation input byte-for-byte', async () => {
    const runtime = useRuntime()
    vi.mocked(request).mockResolvedValueOnce({ id: 'op' })
    await runtime.invoke('counter/read', '{"value":9007199254740993}', 's')
    expect(request).toHaveBeenCalledWith('/operations/counter%2Fread', expect.objectContaining({
      body: '{"sessionId":"s","input":{"value":9007199254740993}}',
    }))
  })

  it('recovers an operation and its form when no operation event arrives', async () => {
    const runtime = useRuntime()
    const invocation: OperationInvocation = { id: 'op', operationId: 'config', name: 'config', status: 'running' }
    const pending: Interaction = { id: 'ask', name: 'config', fields: [], scope: { operation: { invocationId: 'op' } } }
    vi.mocked(request).mockResolvedValueOnce({ ...snapshot(), operationInvocations: [invocation], interactions: [pending] })
    await expect(runtime.refreshOperationState('op')).resolves.toBe(true)
    expect(runtime.operationInvocations.op).toEqual(invocation)
    expect(runtime.interactions.ask).toEqual(pending)
  })

  it('does not restore an interaction resolved after a state refresh began', async () => {
    const runtime = useRuntime()
    const invocation: OperationInvocation = { id: 'op', operationId: 'config', name: 'config', status: 'running' }
    const pending: Interaction = { id: 'ask', name: 'config', fields: [], scope: { operation: { invocationId: 'op' } } }
    runtime.receive(1, { type: 'operation.started', data: invocation })
    runtime.receive(2, { type: 'interaction.requested', data: pending })
    const stale = deferred<Snapshot>()
    vi.mocked(request).mockReturnValueOnce(stale.promise)
    const refresh = runtime.refreshOperationState('op')
    runtime.receive(3, { type: 'interaction.resolved', data: { id: 'ask' } })
    stale.resolve({ ...snapshot(), cursor: 2, operationInvocations: [invocation], interactions: [pending] })
    await refresh
    expect(runtime.interactions.ask).toBeUndefined()
  })

  it('removes a canceled operation from pending requests and ignores late events', async () => {
    const runtime = useRuntime()
    const pending: Interaction = { id: 'ask', name: 'config', fields: [], scope: { operation: { invocationId: 'op' } } }
    runtime.receive(1, { type: 'interaction.requested', data: pending })
    vi.mocked(command).mockResolvedValueOnce(undefined)
    await runtime.cancelOperation('op')
    expect(command).toHaveBeenCalledWith('/operation-invocations/op', 'DELETE')
    expect(runtime.pendingCount).toBe(0)
    runtime.receive(2, { type: 'interaction.requested', data: pending })
    expect(runtime.pendingCount).toBe(0)
  })

  it('counts an operation only after it is suspended and preserves its draft on reconnect', () => {
    const runtime = useRuntime()
    const invocation: OperationInvocation = { id: 'op', operationId: 'config', name: 'config', status: 'running' }
    const pending: Interaction = { id: 'ask', name: 'config', fields: [], scope: { operation: { invocationId: 'op' } } }
    runtime.receive(1, { type: 'operation.started', data: invocation })
    runtime.receive(2, { type: 'interaction.requested', data: pending })
    expect(runtime.pendingCount).toBe(0)
    runtime.interactionDrafts.ask = { changed: true }
    runtime.suspendOperation('op')
    expect(runtime.pendingCount).toBe(1)
    runtime.bootstrap({ ...snapshot(), operationInvocations: [invocation], interactions: [pending] })
    expect(runtime.interactionDrafts.ask).toEqual({ changed: true })
    expect(runtime.pendingCount).toBe(1)
    runtime.resumeOperation('op')
    expect(runtime.pendingCount).toBe(0)
    runtime.receive(5, { type: 'interaction.resolved', data: { id: 'ask' } })
    expect(runtime.interactionDrafts.ask).toBeUndefined()
  })
})

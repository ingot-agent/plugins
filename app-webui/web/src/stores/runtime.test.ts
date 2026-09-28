import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { command, request } from '../api'
import type { Interaction, Message, ModelSelectionSnapshot, OperationInvocation, Session, Snapshot } from '../protocol'
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
const session = (title: string): Session => ({ id: 's', title, createdAt: '2026-01-01', updatedAt: '2026-01-01' })
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

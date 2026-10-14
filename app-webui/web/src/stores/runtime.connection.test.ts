import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { command, request } from '../api'
import { subscribe } from '../sse'
import type { Snapshot } from '../protocol'
import { turnSnapshot } from '../test/turn'
import { useRuntime } from './runtime'

vi.mock('../api', async importOriginal => ({ ...await importOriginal<typeof import('../api')>(), request: vi.fn(), command: vi.fn() }))
vi.mock('../sse', () => ({ subscribe: vi.fn() }))

let state: Snapshot
let runtime: ReturnType<typeof useRuntime>
let connected: Promise<void>
const completed = () => ({ ...turnSnapshot(), revision: 3, status: 'succeeded' as const, historyEnd: 2,
  output: 'Saved answer', blocks: [{ id: 'answer', kind: 'output' as const, text: 'Saved answer', boundary: 0 }],
  outcome: { status: 'succeeded' as const, durationNs: 1000 } })

beforeEach(() => {
  vi.useFakeTimers()
  vi.resetAllMocks()
  setActivePinia(createPinia())
  state = { runtimeId: 'server', cursor: 4, agent: { capabilities: { sendMessage: true } },
    sessions: [], turns: [turnSnapshot()], turnQueues: [{ sessionId: 's', activeId: 'web', paused: false, revision: 1 }],
    interactions: [], interactionStates: [], operations: [], operationInvocations: [] }
  vi.mocked(request).mockImplementation(async path => {
    if (path === '/state') return state
    if (path === '/sessions/s/history') return [
      { role: 'user', content: [{ kind: 'text', text: 'hello' }] },
      { role: 'assistant', content: [{ kind: 'text', text: 'Saved answer' }] },
    ]
    return []
  })
  vi.mocked(subscribe).mockImplementation(async (_cursor, signal, _receive, ready) => {
    ready()
    await new Promise<void>(resolve => signal.addEventListener('abort', () => resolve(), { once: true }))
  })
  runtime = useRuntime()
  connected = runtime.connect()
})

afterEach(async () => {
  runtime.disconnect()
  await connected
  expect(vi.getTimerCount()).toBe(0)
  vi.useRealTimers()
})

it('recovers a missed completion without moving the event cursor or resubmitting input', async () => {
  await vi.advanceTimersByTimeAsync(0)
  runtime.activeSession = 's'
  runtime.interactionDrafts.approval = { answer: 'unsent response' }
  state = { ...state, turns: [completed()], turnQueues: [{ sessionId: 's', paused: false, revision: 3 }] }
  await vi.advanceTimersByTimeAsync(10000)
  expect(runtime.turns.web).toMatchObject({ status: 'succeeded', reconciled: true, output: 'Saved answer' })
  expect(runtime.histories.s).toHaveLength(2)
  expect(runtime.optimistic.web).toBeUndefined()
  expect(runtime.turnQueues.s.activeId).toBeUndefined()
  expect(runtime.interactionDrafts.approval.answer).toBe('unsent response')
  expect(runtime.cursor).toBe(4)
  expect(subscribe).toHaveBeenCalledTimes(1)
  runtime.receive(5, { type: 'agent.runtime.updated', data: { turns: [turnSnapshot()], turnQueues: [] } })
  expect(runtime.turns.web.status).toBe('succeeded')
  const queries = vi.mocked(request).mock.calls.filter(([path]) => path === '/state').length
  await vi.advanceTimersByTimeAsync(20000)
  expect(vi.mocked(request).mock.calls.filter(([path]) => path === '/state')).toHaveLength(queries)
  expect(command).not.toHaveBeenCalled()
})

it('replaces a stalled stream when the server cursor has advanced', async () => {
  await vi.advanceTimersByTimeAsync(0)
  const oldSignal = vi.mocked(subscribe).mock.calls[0][1]
  state = { ...state, cursor: 9, turns: [completed()] }
  await vi.advanceTimersByTimeAsync(10000)
  expect(oldSignal.aborted).toBe(true)
  expect(subscribe).toHaveBeenCalledTimes(2)
  expect(vi.mocked(subscribe).mock.calls[1][0]).toBe(9)
  expect(runtime.turns.web.status).toBe('succeeded')
  expect(runtime.connection).toBe('online')
  expect(command).not.toHaveBeenCalled()
})

it('ignores a slow reconciliation snapshot after newer SSE updates', async () => {
  await vi.advanceTimersByTimeAsync(0)
  let resolve!: (value: Snapshot) => void
  vi.mocked(request).mockReturnValueOnce(new Promise<Snapshot>(done => { resolve = done }))
  await vi.advanceTimersByTimeAsync(10000)
  const reads = vi.mocked(request).mock.calls.length
  window.dispatchEvent(new Event('focus'))
  await vi.advanceTimersByTimeAsync(10000)
  expect(vi.mocked(request).mock.calls).toHaveLength(reads)
  runtime.receive(5, { type: 'agent.runtime.updated', data: { turns: [completed()], turnQueues: [] } })
  resolve(state)
  await vi.advanceTimersByTimeAsync(0)
  expect(runtime.turns.web.status).toBe('succeeded')
  expect(runtime.cursor).toBe(5)
})

it('checks an idle page when it becomes visible without polling idle state', async () => {
  state = { ...state, turns: [] }
  await vi.advanceTimersByTimeAsync(0)
  // The initial request was started before state was changed.
  runtime.turns = {}
  const queries = vi.mocked(request).mock.calls.filter(([path]) => path === '/state').length
  await vi.advanceTimersByTimeAsync(30000)
  expect(vi.mocked(request).mock.calls.filter(([path]) => path === '/state')).toHaveLength(queries)
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  state = { ...state, turns: [completed()] }
  document.dispatchEvent(new Event('visibilitychange'))
  await vi.advanceTimersByTimeAsync(0)
  expect(runtime.turns.web.status).toBe('succeeded')
})

it('bootstraps a restarted server before comparing reset revisions', async () => {
  await vi.advanceTimersByTimeAsync(0)
  runtime.turns.web.revision = 99
  state = { ...state, runtimeId: 'new-server', cursor: 0, turns: [] }
  await vi.advanceTimersByTimeAsync(10000)
  expect(subscribe).toHaveBeenCalledTimes(2)
  expect(runtime.turns).toEqual({})
  expect(runtime.cursor).toBe(0)
})

it('ignores a state response that arrives after disconnect', async () => {
  await vi.advanceTimersByTimeAsync(0)
  let resolve!: (value: Snapshot) => void
  vi.mocked(request).mockReturnValueOnce(new Promise<Snapshot>(done => { resolve = done }))
  await vi.advanceTimersByTimeAsync(10000)
  runtime.disconnect()
  resolve({ ...state, turns: [completed()] })
  await connected
  await vi.advanceTimersByTimeAsync(0)
  expect(runtime.turns.web.status).toBe('running')
  expect(subscribe).toHaveBeenCalledTimes(1)
})

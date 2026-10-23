import { describe, expect, it } from 'vitest'
import { acceptTurn, bootstrapTurns, reduceOperation, reduceTurn } from './state'
import { turnSnapshot } from './test/turn'
import type { LiveTurn, OperationInvocation, Snapshot, Turn, WebEvent } from './protocol'

const update = (...turns: Turn[]): WebEvent => ({ type: 'agent.runtime.updated', data: { turns, turnQueues: [] } })
const bootstrap = (...turns: Turn[]) => bootstrapTurns({ turns, interactions: [] } as unknown as Snapshot)
const scope = { agent: { sessionId: 's', turnId: 'sdk', toolCallId: 'call' } }
const progress = (id: string, revision: number, outputDelta = '', reasoningDelta = '', roundIndex = 0): WebEvent => ({
  type: 'agent.runtime.progress', scope: { agent: { sessionId: 's', turnId: 'sdk', roundIndex } },
  data: { id, revision, outputDelta, reasoningDelta, tools: [] },
})

describe('authoritative turn projections', () => {
  it('restores queued inputs, receives completion and ignores stale command responses', () => {
    const queued = turnSnapshot({ id: 'queued', status: 'queued', started: false, persisted: false, sdkTurnId: undefined, sequence: 2 })
    const active = turnSnapshot({ revision: 3, output: 'answer' })
    const turns = bootstrap(queued, active)
    reduceTurn(turns, update({ ...active, revision: 4, status: 'succeeded', historyEnd: 2 }))
    reduceTurn(turns, update({ ...queued, revision: 4, status: 'running', started: true }))
    acceptTurn(turns, queued)
    acceptTurn(turns, active)
    expect(turns.queued.status).toBe('running')
    expect(turns.web).toMatchObject({ status: 'succeeded', historyEnd: 2 })
  })

  it('keeps a removed queued input outside the execution timeline', () => {
    const queued = turnSnapshot({ id: 'q', status: 'queued', started: false, persisted: false, sdkTurnId: undefined })
    const turns = bootstrap(queued)
    reduceTurn(turns, update({ ...queued, revision: 2, status: 'canceled' }))
    acceptTurn(turns, queued)
    expect(turns.q).toMatchObject({ status: 'canceled', started: false })
  })

  it('uses snapshot blocks without rebuilding content from aggregate text', () => {
    const active = turnSnapshot({ reasoning: 'Earlier thoughts', output: 'Aggregate output', blocks: [
      { id: 'round-0-reasoning', kind: 'reasoning', text: 'Earlier thoughts', boundary: 0 },
      { id: 'round-0-output', kind: 'output', text: 'First round', boundary: 0 },
      { id: 'round-1-output', kind: 'output', text: 'Second round', boundary: 1 },
    ] })
    const turns = bootstrapTurns({ turns: [active], interactions: [{ id: 'pending', scope }] } as unknown as Snapshot)
    expect(turns.web.blocks.map(block => block.kind)).toEqual(['reasoning', 'output', 'output', 'interaction'])
    expect(turns.web.blocks[2]).toMatchObject({ text: 'Second round' })
    expect(active.blocks).toHaveLength(3)
    acceptTurn(turns, { ...active, revision: 2, blocks: [...active.blocks, { id: 'round-2-reasoning', kind: 'reasoning', text: 'New thought', boundary: 2 }] })
    expect(turns.web.blocks.map(block => block.kind)).toEqual(['reasoning', 'output', 'output', 'reasoning', 'interaction'])
    acceptTurn(turns, active)
    expect(turns.web.revision).toBe(2)
  })

  it('retains failure before SDK execution and does not associate observations with it', () => {
    const failed = turnSnapshot({ status: 'failed', persisted: false, sdkTurnId: undefined, error: { code: 'input_failed', message: 'Invalid attachment' } })
    const turns = bootstrap(failed)
    reduceTurn(turns, { type: 'agent.tool.started', scope, data: { call: { id: 'call', name: 'inspect', arguments: {} } } })
    expect(turns.web).toMatchObject({ status: 'failed', blocks: [], error: failed.error })
  })

  it('uses complete command responses and preserves their canonical multimodal result', () => {
    const turns: Record<string, LiveTurn> = {}
    const finished = turnSnapshot({ status: 'succeeded', output: 'partial', result: { output: [{ kind: 'image', source: { kind: 'asset', assetId: 'a' } }] } })
    acceptTurn(turns, finished)
    expect(turns.web.output).toBe('partial')
    expect(turns.web.result?.output[0].kind).toBe('image')
  })

  it('does not regress terminal operations during overlapping replay', () => {
    const operations: Record<string, OperationInvocation> = { op: { id: 'op', operationId: 'test-1', name: 'test', status: 'succeeded', result: { output: {} } } }
    reduceOperation(operations, { type: 'operation.started', data: { id: 'op', operationId: 'test-1', name: 'test', status: 'running' } })
    expect(operations.op.status).toBe('succeeded')
  })

  it('updates tools and associates requests only with the latest admitted input in the matching lifecycle', () => {
    const turns = bootstrap(turnSnapshot({ id: 'A' }), turnSnapshot({ id: 'B', sequence: 2 }), turnSnapshot({ id: 'queued', sequence: 3, status: 'queued', persisted: false, started: false }))
    const tool = { id: 'tool-call', kind: 'tool' as const, call: { id: 'call', name: 'inspect', arguments: {} }, status: 'running' }
    reduceTurn(turns, update(turnSnapshot({ id: 'B', sequence: 2, revision: 2, blocks: [tool] })))
    reduceTurn(turns, { type: 'agent.tool.started', scope, data: { call: { id: 'call', name: 'inspect', arguments: {} } } })
    reduceTurn(turns, { type: 'agent.tool.progress', scope, data: { progress: { content: [{ kind: 'text', text: 'Progress' }] } } })
    const delta = progress('B', 3)
    delta.data.tools = [{ toolCallId: 'call', content: [{ kind: 'text', text: 'Progress' }] }]
    reduceTurn(turns, delta)
    expect(turns.B.blocks[0]).toMatchObject({ content: [{ text: 'Progress' }] })
    reduceTurn(turns, { type: 'interaction.requested', scope, data: { id: 'question' } })
    reduceTurn(turns, { type: 'agent.tool.finished', scope, data: { status: 'succeeded', result: { content: [{ kind: 'text', text: 'Done' }] } } })
    reduceTurn(turns, update(turnSnapshot({ id: 'B', sequence: 2, revision: 4, blocks: [{ ...tool, status: 'succeeded', content: [{ kind: 'text', text: 'Done' }] }] })))
    expect(turns.A.blocks).toEqual([])
    expect(turns.queued.blocks).toEqual([])
    expect(turns.B.blocks).toMatchObject([{ kind: 'tool', status: 'succeeded', content: [{ text: 'Done' }] }, { kind: 'interaction', interactionId: 'question' }])
  })

  it('appends coalesced Unicode progress to its own input and round, once per revision', () => {
    const turns = bootstrap(turnSnapshot({ id: 'A' }), turnSnapshot({ id: 'B', sequence: 2 }))
    reduceTurn(turns, progress('A', 2, '你好🙂', '先思考'))
    reduceTurn(turns, progress('A', 2, '你好🙂', '先思考'))
    reduceTurn(turns, progress('A', 3, '，继续'))
    reduceTurn(turns, progress('B', 4, '第二轮', '', 1))
    expect(turns.A).toMatchObject({ output: '你好🙂，继续', reasoning: '先思考' })
    expect(turns.A.blocks).toMatchObject([{ kind: 'reasoning', boundary: 0 }, { kind: 'output', boundary: 0, text: '你好🙂，继续' }])
    expect(turns.B.blocks).toMatchObject([{ kind: 'output', boundary: 1, text: '第二轮' }])
  })

  it('does not duplicate a batch already covered by a recovery snapshot or regress after completion', () => {
    const turns = bootstrap(turnSnapshot({ revision: 10, output: '已恢复', blocks: [{ id: 'round-0-output', kind: 'output', text: '已恢复', boundary: 0 }] }))
    reduceTurn(turns, progress('web', 10, '已恢复'))
    reduceTurn(turns, progress('web', 9, '更早的文字'))
    reduceTurn(turns, progress('web', 11, '，继续'))
    acceptTurn(turns, turnSnapshot({ revision: 10, output: '已恢复' }))
    expect(turns.web.output).toBe('已恢复，继续')
    acceptTurn(turns, turnSnapshot({ revision: 12, status: 'succeeded', output: '规范结果' }))
    reduceTurn(turns, progress('web', 13, '迟到'))
    expect(turns.web.output).toBe('规范结果')
  })

  it('ignores progress for another session/lifecycle, queued inputs, or unknown receipts', () => {
    const turns = bootstrap(turnSnapshot(), turnSnapshot({ id: 'queued', persisted: false, status: 'queued' }))
    reduceTurn(turns, { ...progress('web', 2, 'wrong'), scope: { agent: { sessionId: 'other', turnId: 'sdk', roundIndex: 0 } } })
    reduceTurn(turns, { ...progress('web', 2, 'wrong'), scope: { agent: { sessionId: 's', turnId: 'old', roundIndex: 0 } } })
    reduceTurn(turns, progress('queued', 2, 'wrong'))
    reduceTurn(turns, progress('missing', 2, 'wrong'))
    expect(turns.web.blocks).toEqual([])
    expect(turns.queued.blocks).toEqual([])
  })

  it('does not guess missing TurnIDs or attach late events to the next invocation', () => {
    const turns = bootstrap(turnSnapshot({ status: 'canceled' }), turnSnapshot({ id: 'next', sdkTurnId: undefined }), turnSnapshot({ id: 'other', sessionId: 'other', sdkTurnId: 'sdk' }))
    const data = { call: { id: 'call', name: 'inspect', arguments: {} } }
    reduceTurn(turns, { type: 'agent.tool.started', scope, data })
    reduceTurn(turns, { type: 'agent.tool.started', scope: { agent: { sessionId: 's', toolCallId: 'call' } }, data })
    reduceTurn(turns, { type: 'interaction.requested', scope: { ...scope, operation: { invocationId: 'op' } }, data: { id: 'op-question' } })
    expect(Object.values(turns).every(turn => turn.blocks.length === 0)).toBe(true)
    expect(turns.next.sdkTurnId).toBeUndefined()
  })

  it('restores separate inputs and round blocks from the same lifecycle', () => {
    const a = turnSnapshot({ id: 'A', input: 'A', revision: 5, output: 'first', blocks: [{ id: 'round-0-output', kind: 'output', text: 'first', boundary: 0 }] })
    const b = turnSnapshot({ id: 'B', input: 'B', sequence: 2, revision: 5 })
    const c = turnSnapshot({ id: 'C', input: 'C', sequence: 3, revision: 5, output: 'next', blocks: [{ id: 'round-1-output', kind: 'output', text: 'next', boundary: 1 }] })
    const turns = bootstrap(a, b, c)
    reduceTurn(turns, update(a, b, { ...c, revision: 6, output: 'final', status: 'succeeded', blocks: [{ id: 'round-1-output', kind: 'output', text: 'final', boundary: 1 }] }))
    reduceTurn(turns, update(c))
    expect(Object.values(turns).map(turn => turn.input)).toEqual(['A', 'B', 'C'])
    expect(turns.A.blocks).toEqual(a.blocks)
    expect(turns.B.blocks).toEqual([])
    expect(turns.C.blocks).toEqual([{ id: 'round-1-output', kind: 'output', text: 'final', boundary: 1 }])
    expect(turns.C.status).toBe('succeeded')
  })
})

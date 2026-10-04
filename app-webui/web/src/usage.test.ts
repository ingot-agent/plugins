import { describe, expect, it } from 'vitest'
import { parseContextUsage } from './usage'
import type { ContextUsage, InteractionState } from './protocol'

const context: ContextUsage = { sessionId: 's', inputTokens: 80, accuracy: 'estimate', source: 'estimator', provider: 'p', model: 'm', turnId: 't', roundIndex: 1 }
const state = (): InteractionState => ({
  id: 'context-compact.session-context/s', name: 'context-compact.session-context/s', scope: { agent: { sessionId: 's' } },
  values: Object.entries(context).map(([name, value]) => ({ name, value })),
})
describe('context snapshots', () => {
  it('preserves accuracy, model identity, Turn and zero counts', () => {
    expect(parseContextUsage(state())).toEqual(context)
    const zero = state()
    zero.values.find(value => value.name === 'inputTokens')!.value = 0
    expect(parseContextUsage(zero)?.inputTokens).toBe(0)
  })

  it('rejects mismatched scopes, duplicate fields and invalid counts', () => {
    const forged = state()
    forged.scope!.agent!.sessionId = 'other'
    const operation = state()
    operation.scope!.operation = { invocationId: 'op' }
    const duplicate = state()
    duplicate.values.push({ name: 'inputTokens', value: 90 })
    for (const invalid of [forged, operation, duplicate, ...[-1, 1.2, '80', Number.MAX_SAFE_INTEGER + 1].map(value => {
      const invalid = state()
      invalid.values.find(entry => entry.name === 'inputTokens')!.value = value
      return invalid
    })]) expect(parseContextUsage(invalid)).toBeUndefined()
  })
})

import type { ContextUsage, InteractionState } from './protocol'

export function isTokenCount(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

export function parseContextUsage(state: InteractionState): ContextUsage | undefined {
  const sessionId = state.scope?.agent?.sessionId
  if (!sessionId || state.scope?.operation || state.id !== state.name || state.name !== 'context-compact.session-context/' + sessionId) return
  const values = new Map<string, unknown>()
  for (const entry of state.values || []) {
    if (values.has(entry.name)) return
    values.set(entry.name, entry.value)
  }
  const inputTokens = values.get('inputTokens')
  const accuracy = values.get('accuracy')
  const source = values.get('source')
  const provider = values.get('provider')
  const model = values.get('model')
  const turnId = values.get('turnId')
  const roundIndex = values.get('roundIndex')
  if (values.get('sessionId') !== sessionId || !isTokenCount(inputTokens) ||
    (accuracy !== 'exact' && accuracy !== 'upper_bound' && accuracy !== 'estimate') ||
    typeof source !== 'string' || !source || typeof provider !== 'string' || !provider || typeof model !== 'string' || !model ||
    (turnId !== undefined && (typeof turnId !== 'string' || !turnId)) ||
    (roundIndex !== undefined && (!turnId || !isTokenCount(roundIndex)))) return
  return { sessionId, inputTokens, accuracy, source, provider, model, turnId, roundIndex } as ContextUsage
}

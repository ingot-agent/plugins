import type { LiveTurn, OperationInvocation, Part, Snapshot, Turn, WebEvent } from './protocol'

export const indexById = <T extends { id: string }>(items: T[] | null | undefined): Record<string, T> =>
  Object.fromEntries((items || []).map(item => [item.id, item]))

export function bootstrapTurns(snapshot: Snapshot): Record<string, LiveTurn> {
  const turns = indexById(snapshot.turns.map(turn => ({ ...turn, blocks: turn.blocks.map(block => ({ ...block })) })))
  for (const interaction of snapshot.interactions) {
    reduceTurn(turns, { type: 'interaction.requested', scope: interaction.scope, data: interaction })
  }
  return turns
}

function activityTurn(turns: Record<string, LiveTurn>, event: WebEvent): LiveTurn | undefined {
  const scope = event.scope?.agent || event.data.scope?.agent
  if (!scope?.sessionId || !scope.turnId || event.scope?.operation || event.data.scope?.operation) return
  const matched = Object.values(turns).filter(turn => turn.sessionId === scope.sessionId && turn.sdkTurnId === scope.turnId && turn.persisted)
    .sort((a, b) => b.sequence - a.sequence)[0]
  return matched?.status === 'running' ? matched : undefined
}

export function reduceTurn(turns: Record<string, LiveTurn>, event: WebEvent) {
  const data = event.data
  if (event.type === 'agent.runtime.updated') {
    for (const snapshot of data.turns as Turn[]) acceptTurn(turns, snapshot)
    return
  }
  if (event.type === 'agent.runtime.progress') {
    const turn = turns[data.id]
    const scope = event.scope?.agent
    if (!turn || turn.status !== 'running' || !turn.persisted || turn.sessionId !== scope?.sessionId ||
      turn.sdkTurnId !== scope?.turnId || scope.roundIndex === undefined || data.revision <= turn.revision) return
    turn.revision = data.revision
    for (const kind of ['reasoning', 'output'] as const) {
      const delta = data[kind + 'Delta'] as string
      if (!delta) continue
      const id = `round-${scope.roundIndex}-${kind}`
      const block = turn.blocks.find(block => block.id === id)
      if (block?.kind === kind) block.text += delta
      else turn.blocks.push({ id, kind, text: delta, boundary: scope.roundIndex })
      turn[kind] += delta
    }
    for (const delta of data.tools as { toolCallId: string; content: Part[] }[]) {
      const block = turn.blocks.find(block => block.kind === 'tool' && block.call.id === delta.toolCallId)
      if (block?.kind === 'tool') block.content = [...(block.content || []), ...delta.content]
    }
    return
  }
  // Observation events remain diagnostic traces. Runtime updates are the sole
  // source of text/tool state, preventing double-appends across the two streams.
  if (event.type !== 'interaction.requested') return
  const turn = activityTurn(turns, event)
  if (!turn) return
  const blocks = turn.blocks
  if (!blocks.some(block => block.kind === 'interaction' && block.interactionId === data.id)) {
    blocks.push({ id: 'interaction-' + data.id, kind: 'interaction', interactionId: data.id })
  }
}

// Command responses may arrive after their SSE events or after completion.
export function acceptTurn(turns: Record<string, LiveTurn>, snapshot: Turn) {
  const previous = turns[snapshot.id]
  if (previous && snapshot.revision <= previous.revision) return
  const blocks = snapshot.blocks.map(block => ({ ...block }))
  for (const block of previous?.blocks || []) {
    if (block.kind === 'interaction' && !blocks.some(item => item.id === block.id)) blocks.push(block)
  }
  turns[snapshot.id] = { ...previous, ...snapshot, retry: snapshot.retry, blocks, stopping: previous?.stopping && snapshot.status === 'running' }
}

export function reduceOperation(items: Record<string, OperationInvocation>, event: WebEvent) {
  const next = event.data as OperationInvocation
  if (event.type === 'operation.started' && items[next.id]?.status !== undefined && items[next.id].status !== 'running') return
  items[next.id] = next
}

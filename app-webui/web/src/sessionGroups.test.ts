import { describe, expect, it } from 'vitest'
import { GROUP_SESSION_LIMIT, groupSessions, isGroupCollapsed, visibleGroupSessions } from './sessionGroups'
import type { GroupView } from './sessionGroups'
import type { Session } from './protocol'

function session(id: string, workspace: string, updatedAt = '2025-01-01T00:00:00Z'): Session {
  return { id, title: id, workspace, updatedAt } as Session
}

const view = (overrides: Record<string, boolean> = {}, extra: Partial<GroupView> = {}): GroupView =>
  ({ overrides: new Map(Object.entries(overrides)), filtering: false, ...extra })

describe('groupSessions', () => {
  it('preserves session order and groups by workspace', () => {
    const groups = groupSessions([
      session('a1', '/repo/alpha'),
      session('b1', '/repo/beta'),
      session('a2', '/repo/alpha'),
    ], 'No workspace')
    expect(groups.map(group => group.key)).toEqual(['/repo/alpha', '/repo/beta'])
    expect(groups[0].label).toBe('alpha')
    expect(groups[0].sessions.map(item => item.id)).toEqual(['a1', 'a2'])
  })

  it('puts sessions without a workspace under a single labelled group', () => {
    const groups = groupSessions([session('one', ''), session('two', '')], 'No workspace')
    expect(groups).toHaveLength(1)
    expect(groups[0].label).toBe('No workspace')
    expect(groups[0].sessions).toHaveLength(2)
  })
})

describe('workspace folding', () => {
  const many = Array.from({ length: GROUP_SESSION_LIMIT + 3 }, (_, index) => session('s' + index, '/repo/alpha'))
  const group = groupSessions(many, 'No workspace')[0]

  it('folds workspaces above the limit until expanded', () => {
    expect(isGroupCollapsed(group, view())).toBe(true)
    expect(visibleGroupSessions(group, view())).toHaveLength(GROUP_SESSION_LIMIT)
    expect(visibleGroupSessions(group, view({ [group.key]: true }))).toHaveLength(GROUP_SESSION_LIMIT + 3)
  })

  it('does not fold at the limit', () => {
    const atLimit = groupSessions(many.slice(0, GROUP_SESSION_LIMIT), 'No workspace')[0]
    expect(isGroupCollapsed(atLimit, view())).toBe(false)
    expect(visibleGroupSessions(atLimit, view())).toHaveLength(GROUP_SESSION_LIMIT)
  })

  it('keeps every match visible while searching', () => {
    expect(isGroupCollapsed(group, view({}, { filtering: true }))).toBe(false)
    expect(visibleGroupSessions(group, view({}, { filtering: true }))).toHaveLength(GROUP_SESSION_LIMIT + 3)
  })

  it('keeps the conversation on screen expanded', () => {
    expect(isGroupCollapsed(group, view({}, { activeSessionId: 's9' }))).toBe(false)
    expect(isGroupCollapsed(group, view({}, { activeSessionId: 'elsewhere' }))).toBe(true)
  })

  it('honours an explicit collapse even for the active conversation', () => {
    expect(isGroupCollapsed(group, view({ [group.key]: false }, { activeSessionId: 's9' }))).toBe(true)
  })
})

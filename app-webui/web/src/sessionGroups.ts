import { workspaceBasename } from './time'
import type { Session } from './protocol'

// A workspace with more conversations than this is collapsed by default so the
// sidebar stays scannable; the rest are disclosed on demand.
export const GROUP_SESSION_LIMIT = 7

export interface SessionGroup {
  key: string
  label: string
  sessions: Session[]
}

export interface GroupView {
  // Explicit user toggles, keyed by workspace. A stored false means the user
  // folded the group themselves, which wins over every default.
  overrides: ReadonlyMap<string, boolean>
  // A search already narrows the list, so paging it again would hide matches.
  filtering: boolean
  // The conversation on screen must never disappear behind a folded group.
  activeSessionId?: string
}

// Group sessions by workspace root, preserving most-recent session order.
// Sessions without a binding collapse under a single "no workspace" group.
export function groupSessions(sessions: Session[], unnamedLabel: string): SessionGroup[] {
  const ordered: SessionGroup[] = []
  const byKey = new Map<string, SessionGroup>()
  for (const session of sessions) {
    const key = session.workspace || ''
    const label = key ? workspaceBasename(key) : unnamedLabel
    let group = byKey.get(key)
    if (!group) {
      group = { key, label, sessions: [] }
      byKey.set(key, group)
      ordered.push(group)
    }
    group.sessions.push(session)
  }
  return ordered
}

export function isGroupCollapsed(group: SessionGroup, view: GroupView): boolean {
  if (view.filtering) return false
  const override = view.overrides.get(group.key)
  if (override !== undefined) return !override
  if (view.activeSessionId && group.sessions.some(session => session.id === view.activeSessionId)) return false
  return group.sessions.length > GROUP_SESSION_LIMIT
}

export function visibleGroupSessions(group: SessionGroup, view: GroupView): Session[] {
  return isGroupCollapsed(group, view) ? group.sessions.slice(0, GROUP_SESSION_LIMIT) : group.sessions
}

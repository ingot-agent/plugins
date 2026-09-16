import type { Operation } from './protocol'

export interface OperationGroup {
  name: string
  operations: Operation[]
}

export type ParsedComposerInput =
  | { kind: 'message'; text: string }
  | { kind: 'group-search'; query: string; exact?: OperationGroup }
  | { kind: 'operation-search'; group: OperationGroup; query: string; exact?: Operation }
  | { kind: 'command'; operation: Operation }
  | { kind: 'invalid-command'; reason: 'unknown-group' | 'unknown-operation' | 'trailing-input'; group?: string; operation?: string }

export function operationGroups(operations: Operation[]): OperationGroup[] {
  const groups = new Map<string, Operation[]>()
  for (const operation of operations) {
    const items = groups.get(operation.group) || []
    items.push(operation)
    groups.set(operation.group, items)
  }
  return [...groups].map(([name, items]) => ({
    name,
    operations: items.slice().sort((left, right) => left.name.localeCompare(right.name)),
  })).sort((left, right) => left.name.localeCompare(right.name))
}

export function parseComposerInput(text: string, operations: Operation[]): ParsedComposerInput {
  if (!text.startsWith('/') || text.includes('\n')) return { kind: 'message', text }
  if (text.startsWith('//')) return { kind: 'message', text: text.slice(1) }

  const groups = operationGroups(operations)
  const separator = text.indexOf(' ')
  if (separator < 0) {
    const query = text.slice(1)
    return { kind: 'group-search', query, exact: groups.find(group => group.name === query) }
  }

  const groupName = text.slice(1, separator)
  const group = groups.find(item => item.name === groupName)
  if (!group) return { kind: 'invalid-command', reason: 'unknown-group', group: groupName }

  const remainder = text.slice(separator).trimStart()
  const query = remainder.trimEnd()
  if (/\s/.test(query)) return { kind: 'invalid-command', reason: 'trailing-input', group: groupName, operation: query }
  const exact = group.operations.filter(operation => operation.name === query)
  if (exact.length === 1) return { kind: 'command', operation: exact[0] }
  if (exact.length > 1) return { kind: 'operation-search', group, query }
  if (!query || group.operations.some(operation => matches(operation.name, operation.description, query))) {
    return { kind: 'operation-search', group, query }
  }
  return { kind: 'invalid-command', reason: 'unknown-operation', group: groupName, operation: query }
}

export function filterGroups(groups: OperationGroup[], query: string): OperationGroup[] {
  return rank(groups, query, group => group.name, group => group.operations.map(item => item.description).join(' '))
}

export function filterOperations(operations: Operation[], query: string): Operation[] {
  return rank(operations, query, item => item.name, item => item.description)
}

function matches(name: string, description: string, query: string) {
  const needle = query.toLocaleLowerCase()
  return name.toLocaleLowerCase().includes(needle) || description.toLocaleLowerCase().includes(needle)
}

function rank<T>(items: T[], query: string, name: (item: T) => string, description: (item: T) => string): T[] {
  if (!query) return items
  const needle = query.toLocaleLowerCase()
  return items.filter(item => matches(name(item), description(item), query)).sort((left, right) => {
    const score = (item: T) => {
      const value = name(item).toLocaleLowerCase()
      if (value.startsWith(needle)) return 0
      if (value.includes(needle)) return 1
      return 2
    }
    return score(left) - score(right) || name(left).localeCompare(name(right))
  })
}

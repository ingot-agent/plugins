import { describe, expect, it } from 'vitest'
import { filterGroups, operationGroups, parseComposerInput } from './commands'
import type { Operation } from './protocol'

const operation = (group: string, name: string, description = ''): Operation => ({
  id: group + '-' + name, group, name, description, inputSchema: {}, outputSchema: {},
})
const operations = [
  operation('tool-shell', 'config', 'Configure shell execution'),
  operation('tool-shell', 'status', 'Inspect shell status'),
  operation('tool-ask', 'config', 'Configure questions'),
]

describe('slash command parsing', () => {
  it('discovers groups before operations and keeps duplicate local names', () => {
    expect(operationGroups(operations).map(group => group.name)).toEqual(['tool-ask', 'tool-shell'])
    expect(parseComposerInput('/', operations)).toMatchObject({ kind: 'group-search', query: '' })
    expect(parseComposerInput('/tool-shell ', operations)).toMatchObject({ kind: 'operation-search', query: '' })
    expect(parseComposerInput('/tool-shell config', operations)).toMatchObject({ kind: 'command', operation: { id: 'tool-shell-config' } })
  })

  it('never treats unknown slash input as a model message and supports escaping', () => {
    expect(parseComposerInput('/unknown', operations)).toMatchObject({ kind: 'group-search', query: 'unknown' })
    expect(parseComposerInput('/unknown config', operations)).toMatchObject({ kind: 'invalid-command', reason: 'unknown-group' })
    expect(parseComposerInput('/tool-shell unknown', operations)).toMatchObject({ kind: 'invalid-command', reason: 'unknown-operation' })
    expect(parseComposerInput('//usr/local/bin', operations)).toEqual({ kind: 'message', text: '/usr/local/bin' })
  })

  it('ranks prefix matches before descriptions', () => {
    const groups = operationGroups(operations)
    expect(filterGroups(groups, 'tool-s').map(group => group.name)).toEqual(['tool-shell'])
    expect(filterGroups(groups, 'questions').map(group => group.name)).toEqual(['tool-ask'])
  })
})

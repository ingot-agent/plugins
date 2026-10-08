import { describe, expect, it } from 'vitest'
import { displayMessage, shouldShowHistoryMessage, shouldShowTurnByline } from './conversationDisplay'
import type { Interaction, LiveTurn, Message } from '../protocol'

const toolMessage: Message = { role: 'assistant', content: [], toolCalls: [{ id: 'call', name: 'inspect', arguments: {} }] }
const answerWithTool: Message = { role: 'assistant', content: [{ kind: 'text', text: 'I will inspect this.' }], toolCalls: toolMessage.toolCalls }
const pluginMessage: Message = { role: 'user', content: [{ kind: 'text', text: '<system source="plugin">\nlocal files &amp; context\n</system>' }] }
const interaction: Interaction = { id: 'question', name: 'ask', fields: [] }
const toolOnlyTurn: LiveTurn = { id: 'turn', sessionId: 'session', revision: 0, output: '', reasoning: '', status: 'running', blocks: [{ id: 'tool', kind: 'tool', call: toolMessage.toolCalls![0], status: 'running' }] }
const file = { kind: 'file', name: 'report & notes.pdf', mimeType: 'application/pdf', path: 'C:\\reports\\report & notes.pdf' }

function filesMessage(files: unknown, notice = '\u4ee5\u4e0a\u662f\u7d27\u968f\u5176\u540e\u7684\u7528\u6237\u8f93\u5165\u6240\u4e0a\u4f20\u7684\u6587\u4ef6\u3002'): Message {
  const envelope = document.implementation.createDocument('', 'system').documentElement
  envelope.setAttribute('source', 'plugin')
  envelope.textContent = JSON.stringify(files) + '\n\n' + notice
  return { role: 'user', content: [{ kind: 'text', text: new XMLSerializer().serializeToString(envelope) }] }
}

describe('conversation display projection', () => {
  it('hides only tool-only history messages when tool calls are hidden', () => {
    expect(shouldShowHistoryMessage(toolMessage, false)).toBe(false)
    expect(shouldShowHistoryMessage(toolMessage, true)).toBe(true)
    expect(shouldShowHistoryMessage(answerWithTool, false)).toBe(true)
  })

  it('hides plugin context regardless of the tool display preference and retains empty user input', () => {
    expect(shouldShowHistoryMessage(pluginMessage, false)).toBe(false)
    expect(shouldShowHistoryMessage(pluginMessage, true)).toBe(false)
    expect(shouldShowHistoryMessage({ role: 'user', content: [] }, false)).toBe(true)
    expect(pluginMessage.content[0]!.text).toContain('local files &amp; context')
  })

  it.each([
    '<system>ordinary user text</system>',
    '<system source="user">ordinary user text</system>',
    '<system source="">missing source label</system>',
    '<system source="plugin">unclosed',
    '<system source="plugin"><nested>example</nested></system>',
    '```xml\n<system source="plugin">example</system>\n```',
    'Example: <system source="plugin">context</system>',
  ])('keeps ordinary or malformed user text visible: %s', text => {
    expect(shouldShowHistoryMessage({ role: 'user', content: [{ kind: 'text', text }] }, true)).toBe(true)
  })

  it('keeps assistant explanations and media messages containing the marker visible', () => {
    expect(shouldShowHistoryMessage({ ...pluginMessage, role: 'assistant' }, true)).toBe(true)
    expect(shouldShowHistoryMessage({ ...pluginMessage, content: [...pluginMessage.content, { kind: 'image' }] }, true)).toBe(true)
  })

  it('renders files on an empty user message without mutating context', () => {
    const user: Message = { role: 'user', content: [] }
    const notice = filesMessage([file])
    const originalNotice = notice.content[0]!.text
    expect(displayMessage(user, notice).content).toEqual([
      { kind: 'file', name: file.name, mimeType: file.mimeType, source: { kind: 'local', path: file.path } },
    ])
    expect(user.content).toEqual([])
    expect(notice.content[0]!.text).toBe(originalNotice)
    expect(shouldShowHistoryMessage(notice, true)).toBe(false)
  })

  it('associates each file notice with the following user input', () => {
    const previous: Message = { role: 'user', content: [{ kind: 'text', text: 'Previous input.' }] }
    const current: Message = { role: 'user', content: [] }
    const next: Message = { role: 'user', content: [{ kind: 'text', text: 'Next input.' }] }
    const history = [previous, filesMessage([file]), current, next]
    const displayed = history.map((message, index) => displayMessage(message, history[index - 1]))
    expect(displayed[0]).toBe(previous)
    expect(displayed[2]!.content[0]?.source).toEqual({ kind: 'local', path: file.path })
    expect(displayed[3]).toBe(next)
    expect(current.content).toEqual([])
  })

  it('keeps text and mixed attachments in selection order with image assets intact', () => {
    const image = { kind: 'image', name: 'pixel.png', mimeType: 'image/png', source: { kind: 'asset', assetId: 'pixel' } }
    const user: Message = { role: 'user', content: [{ kind: 'text', text: 'Read these files.' }, image] }
    const notice = filesMessage([file, { kind: 'image', name: image.name, mimeType: image.mimeType, path: 'C:\\reports\\pixel.png' }, file])
    const displayed = displayMessage(user, notice)
    expect(displayed.content.map(part => part.name || part.text)).toEqual(['Read these files.', file.name, 'pixel.png', file.name])
    expect(displayed.content[2]).toBe(image)
    expect(user.content).toEqual([{ kind: 'text', text: 'Read these files.' }, image])
  })

  it('leaves unrelated messages and plugin notices unchanged', () => {
    const user: Message = { role: 'user', content: [] }
    const notice = filesMessage([file])
    expect(displayMessage(user)).toBe(user)
    expect(displayMessage(user, filesMessage([file], 'Other context.'))).toBe(user)
    expect(displayMessage(user, filesMessage([file], ''))).toBe(user)
    expect(displayMessage(user, pluginMessage)).toBe(user)
    expect(displayMessage(pluginMessage, notice)).toBe(pluginMessage)
    expect(displayMessage(answerWithTool, notice)).toBe(answerWithTool)
  })

  it.each([null, {}, [null], [{ kind: 'file' }], [file, { ...file, path: 42 }]])('ignores invalid file metadata: %j', files => {
    const user: Message = { role: 'user', content: [] }
    expect(displayMessage(user, filesMessage(files))).toBe(user)
  })

  it('keeps a live turn byline for visible output and interactions', () => {
    expect(shouldShowTurnByline(toolOnlyTurn, false, {})).toBe(false)
    expect(shouldShowTurnByline(toolOnlyTurn, true, {})).toBe(true)

    const turnWithInteraction: LiveTurn = { ...toolOnlyTurn, blocks: [...toolOnlyTurn.blocks!, { id: 'interaction', kind: 'interaction', interactionId: 'question' }] }
    expect(shouldShowTurnByline(turnWithInteraction, false, { question: interaction })).toBe(true)
  })
})

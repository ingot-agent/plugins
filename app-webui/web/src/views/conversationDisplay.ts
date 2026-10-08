import type { Attachment, Interaction, LiveTurn, Message, Part } from '../protocol'

const uploadedFilesNotice = '\u4ee5\u4e0a\u662f\u7d27\u968f\u5176\u540e\u7684\u7528\u6237\u8f93\u5165\u6240\u4e0a\u4f20\u7684\u6587\u4ef6\u3002'

export function hasVisibleMessageContent(message: Message) {
  return message.content.some(part => part.kind !== 'text' || Boolean(part.text?.trim()))
}

function pluginContextEnvelope(message?: Message) {
  if (!message || message.role !== 'user' || message.toolCallId || message.toolCalls?.length || message.content.length !== 1) return undefined
  const part = message.content[0]!
  const text = part.kind === 'text' ? part.text?.trim() : undefined
  if (!text?.startsWith('<system')) return undefined
  const envelope = new DOMParser().parseFromString(text, 'application/xml').documentElement
  if (envelope.tagName === 'system' && envelope.getAttribute('source') === 'plugin' && envelope.children.length === 0) return envelope
}

export function isPluginContextMessage(message: Message) {
  return Boolean(pluginContextEnvelope(message))
}

function isUploadedFile(value: unknown): value is Attachment {
  if (!value || typeof value !== 'object') return false
  return 'kind' in value && (value.kind === 'file' || value.kind === 'image') &&
    'path' in value && typeof value.path === 'string' && Boolean(value.path) &&
    'name' in value && typeof value.name === 'string' && Boolean(value.name) &&
    'mimeType' in value && typeof value.mimeType === 'string'
}

function uploadedFiles(message?: Message): Attachment[] {
  const envelope = pluginContextEnvelope(message)
  if (!envelope) return []
  const text = envelope.textContent?.trim() || ''
  if (!text.endsWith(uploadedFilesNotice)) return []
  try {
    const files: unknown = JSON.parse(text.slice(0, -uploadedFilesNotice.length).trimEnd())
    return Array.isArray(files) && files.every(isUploadedFile) ? files : []
  } catch { return [] }
}

// Project local files for display without adding media to stored/model messages.
export function displayMessage(message: Message, preceding?: Message): Message {
  if (message.role !== 'user' || isPluginContextMessage(message)) return message
  const files = uploadedFiles(preceding)
  if (!files.length) return message
  const images = message.content.filter(part => part.kind === 'image')
  const attachments = files.map((file): Part => {
    const imageIndex = file.kind === 'image'
      ? images.findIndex(part => part.name === file.name && part.mimeType === file.mimeType) : -1
    if (imageIndex >= 0) return images.splice(imageIndex, 1)[0]!
    return { kind: 'file', name: file.name, mimeType: file.mimeType, source: { kind: 'local', path: file.path } }
  })
  return { ...message, content: [...message.content.filter(part => part.kind !== 'image'), ...attachments, ...images] }
}

export function shouldShowHistoryMessage(message: Message, showToolCalls: boolean) {
  if (isPluginContextMessage(message)) return false
  const isToolOnlyAssistant = message.role === 'assistant' && Boolean(message.toolCalls?.length) && !hasVisibleMessageContent(message)
  return showToolCalls || !isToolOnlyAssistant
}

export function shouldShowTurnByline(turn: LiveTurn, showToolCalls: boolean, interactions: Record<string, Interaction>) {
  if (showToolCalls) return true
  const blocks = turn.blocks || []
  if (!blocks.some(block => block.kind === 'tool')) return true
  if (turn.output || turn.reasoning || turn.result?.output?.length || turn.error) return true
  return blocks.some(block => block.kind !== 'tool' && (block.kind !== 'interaction' || Boolean(interactions[block.interactionId])))
}

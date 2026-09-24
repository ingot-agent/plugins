<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Activity, ArrowDown, ChevronRight, Copy, FolderOpen, Grip, LoaderCircle, MessageCircleQuestion, Terminal, X } from 'lucide-vue-next'
import { useRuntime } from '../stores/runtime'
import { APIError, errorMessage } from '../api'
import { turnCopyTexts } from '../copy'
import { titleForFirstMessage } from '../title'
import type { Attachment, Followup, FollowupAnchor, LiveTurn, Message, Operation, Part } from '../protocol'
import Brand from '../components/Brand.vue'
import Composer from '../components/Composer.vue'
import ContentParts from '../components/ContentParts.vue'
import TurnContent from '../components/TurnContent.vue'
import ToolCard from '../components/ToolCard.vue'
import InteractionCard from '../components/InteractionCard.vue'
import ExecutionPanel from '../components/ExecutionPanel.vue'
import SessionMenu from '../components/SessionMenu.vue'
import StatusBadge from '../components/StatusBadge.vue'
import Overlay from '../components/Overlay.vue'
import JsonBlock from '../components/JsonBlock.vue'
import WorkspaceHeader from '../components/WorkspaceHeader.vue'
import FollowupNote from '../components/FollowupNote.vue'
import { readPreference, savePreference } from '../theme'
import { shouldShowTurnByline } from './conversationDisplay'
defineEmits<{ navigation: []; pending: []; operation: [operation: Operation, sessionId: string] }>()
const runtime = useRuntime()
const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const sessionId = computed(() => typeof route.params.id === 'string' ? route.params.id : '')
const session = computed(() => runtime.sessions.find(item => item.id === sessionId.value))
const running = computed(() => runtime.running(sessionId.value))
const messages = computed(() => runtime.histories[sessionId.value] || [])
const followups = computed(() => runtime.followups[sessionId.value] || [])
const selectionAction = ref<{ anchor: FollowupAnchor; x: number; y: number }>()
type FollowupWindow = { id: string; x: number; y: number; width: number; height: number; z: number }
const openWindows = ref<FollowupWindow[]>([])
const visibleWindows = computed(() => openWindows.value.flatMap(window => {
  const note = followups.value.find(item => item.id === window.id)
  return note ? [{ window, note }] : []
}))
const noteRanges = new Map<string, Range>()
let topWindow = 20
const openingFollowup = ref(false)
let windowGesture: { id: string; kind: 'move' | 'resize'; x: number; y: number; original: FollowupWindow } | undefined
const liveTurns = computed(() => Object.values(runtime.turns).filter(turn => turn.sessionId === sessionId.value))
const requests = computed(() => Object.values(runtime.interactions).filter(item => item.scope?.agent?.sessionId === sessionId.value))
const hostStates = computed(() => Object.values(runtime.interactionStates).filter(item => item.scope?.agent?.sessionId === sessionId.value || !item.scope))
const sending = ref(false)
const details = ref(readPreference('details', 'closed') === 'open')
const showToolCalls = ref(readPreference('tool-calls', 'visible') === 'visible')
// An explicit selection applies only to the pending new Session. Empty means
// the server-owned default Workspace will be bound when the Session is created.
const workspace = ref('')
const selectingWorkspace = ref(false)
const assigningWorkspace = ref(false)
const needsWorkspace = computed(() => Boolean(session.value && !session.value.workspace))
const effectiveWorkspace = computed(() => workspace.value.trim() || runtime.defaultWorkspace)
async function selectWorkspace(path: string) {
  workspace.value = path
  if (!needsWorkspace.value || !sessionId.value) return
  assigningWorkspace.value = true
  try {
    await runtime.assignWorkspace(sessionId.value, path)
  } catch (error) {
    runtime.notify(errorMessage(error))
  } finally {
    assigningWorkspace.value = false
  }
}
async function chooseWorkspace() {
  selectingWorkspace.value = true
  try {
    const result = await runtime.pickWorkspace(effectiveWorkspace.value)
    if (result.path) await selectWorkspace(result.path)
  } catch (error) {
    runtime.notify(errorMessage(error))
  } finally {
    selectingWorkspace.value = false
  }
}
const narrow = ref(window.matchMedia('(max-width: 1199px)').matches)
const chatMain = ref<HTMLElement>()
const scroll = ref<HTMLElement>()
const composerDock = ref<HTMLElement>()
let composerObserver: ResizeObserver | undefined
const following = ref(true)
type TranscriptEntry = { id: string; kind: 'message'; message: Message; index: number } | { id: string; kind: 'turn'; turn: LiveTurn }
const transcript = computed(() => {
  const entries: TranscriptEntry[] = []
  const settled = liveTurns.value.filter(turn => turn.reconciled)
  const appendTurns = (end: number) => {
    for (const turn of settled.filter(turn => turn.historyEnd === end)) entries.push({ id: turn.id, kind: 'turn', turn })
  }
  appendTurns(0)
  messages.value.forEach((message, index) => {
    if (message.role !== 'tool') entries.push({ id: 'history-' + index, kind: 'message', message, index })
    appendTurns(index + 1)
  })
  for (const turn of liveTurns.value.filter(turn => !turn.reconciled)) {
    const local = runtime.optimistic[turn.id]
    if (local) entries.push({ id: 'local-' + turn.id, kind: 'message', message: local.message, index: -1 })
    entries.push({ id: turn.id, kind: 'turn', turn })
  }
  return entries
})
const turnCopies = computed(() => turnCopyTexts(messages.value))
const toolResults = computed(() => new Map(messages.value.filter(message => message.role === 'tool').map(message => [message.toolCallId, message])))
const historicalToolIds = computed(() => new Set(messages.value.flatMap(message => (message.toolCalls || []).map(call => call.id))))
const timelineToolIds = computed(() => new Set(liveTurns.value.flatMap(turn => (turn.blocks || []).flatMap(block => block.kind === 'tool' ? [block.call.id] : []))))
const toolCalls = computed(() => {
  const calls = new Map<string, { id: string; name: string; arguments: unknown; content?: Part[]; status: string; error?: string }>()
  for (const event of runtime.traces[sessionId.value] || []) {
    const id = event.scope?.agent?.toolCallId
    if (!id) continue
    if (event.type === 'agent.tool.started') calls.set(id, { ...event.data.call, status: 'running' })
    const call = calls.get(id)
    if (event.type === 'agent.tool.finished' && call) { call.status = event.data.status; call.content = event.data.result?.content; call.error = event.data.error }
    if (event.type === 'agent.tool.progress' && call) call.content = [...(call.content || []), ...(event.data.progress?.content || [])]
  }
  return [...calls.values()].filter(call => !historicalToolIds.value.has(call.id) && !timelineToolIds.value.has(call.id))
})
const showTurn = (turn: LiveTurn) => !turn.reconciled || (turn.status !== 'succeeded' && !!turn.output)
const timelineRequestIds = computed(() => new Set(liveTurns.value.filter(showTurn).flatMap(turn => (turn.blocks || []).flatMap(block => block.kind === 'interaction' ? [block.interactionId] : []))))
const looseRequests = computed(() => requests.value.filter(item => !timelineRequestIds.value.has(item.id)))
const welcome = computed(() => !sessionId.value)
const media = window.matchMedia('(max-width: 1199px)')
function resize() { narrow.value = media.matches }
media.addEventListener('change', resize)
function syncComposerSpace() {
  if (chatMain.value && composerDock.value) chatMain.value.style.setProperty('--composer-space', `${composerDock.value.offsetHeight}px`)
}
onMounted(() => {
  syncComposerSpace()
  if (composerDock.value && typeof ResizeObserver !== 'undefined') {
    composerObserver = new ResizeObserver(syncComposerSpace)
    composerObserver.observe(composerDock.value)
  }
})
watch(details, value => savePreference('details', value ? 'open' : 'closed'))
watch(showToolCalls, value => savePreference('tool-calls', value ? 'visible' : 'hidden'))
watch(sessionId, id => {
  runtime.activeSession = id
  following.value = true
  openWindows.value = []
  selectionAction.value = undefined
  if (id) {
    void runtime.loadHistory(id)
    void runtime.loadFollowups(id).catch(error => runtime.notify(errorMessage(error)))
  }
}, { immediate: true })
function captureSelection() {
  const selection = window.getSelection()
  if (!selection || selection.isCollapsed || !selection.rangeCount) { selectionAction.value = undefined; return }
  const range = selection.getRangeAt(0)
  const start = range.startContainer instanceof Element ? range.startContainer : range.startContainer.parentElement
  const end = range.endContainer instanceof Element ? range.endContainer : range.endContainer.parentElement
  const markdown = start?.closest<HTMLElement>('.message-assistant[data-message-index] .markdown[data-part-index]')
  if (!markdown || !markdown.contains(range.startContainer) || !markdown.contains(range.endContainer) || end?.closest('.code-toolbar')) return
  const article = markdown.closest<HTMLElement>('[data-message-index]')
  const messageIndex = Number(article?.dataset.messageIndex)
  const partIndex = Number(markdown.dataset.partIndex)
  const selected = range.toString()
  const quote = selected.trim()
  if (!article || !Number.isInteger(messageIndex) || !Number.isInteger(partIndex) || !quote || quote.length > 2000) return
  const prefix = document.createRange()
  prefix.selectNodeContents(markdown)
  prefix.setEnd(range.startContainer, range.startOffset)
  const offset = prefix.toString().length + (selected.length - selected.trimStart().length)
  const rect = range.getBoundingClientRect()
  selectionAction.value = { anchor: { messageIndex, partIndex, start: offset, end: offset + quote.length, quote }, x: rect.right, y: rect.bottom }
}
function selectionPosition(x: number, y: number) {
  return { left: `${Math.max(12, Math.min(x + 12, window.innerWidth - 126))}px`, top: `${Math.max(12, Math.min(y + 8, window.innerHeight - 48))}px` }
}
async function openDraft() {
  if (!selectionAction.value) return
  openingFollowup.value = true
  try {
    const note = await runtime.createFollowup(sessionId.value, selectionAction.value.anchor)
    openFollowup(note, { x: selectionAction.value.x, y: selectionAction.value.y })
    selectionAction.value = undefined
    window.getSelection()?.removeAllRanges()
  } catch (error) { runtime.notify(errorMessage(error)) }
  finally { openingFollowup.value = false }
}
function clamp(value: number, min: number, max: number) { return Math.max(min, Math.min(max, value)) }
function windowSize() {
  return { width: Math.min(420, window.innerWidth - 24), height: Math.min(470, window.innerHeight - 24) }
}
function focusWindow(id: string) {
  const item = openWindows.value.find(window => window.id === id)
  if (item) item.z = ++topWindow
}
function openFollowup(note: Followup, point?: { x: number; y: number }) {
  const existing = openWindows.value.find(window => window.id === note.id)
  if (existing) { focusWindow(note.id); return }
  const { width, height } = windowSize()
  const offset = (openWindows.value.length % 5) * 28
  const x = point?.x ?? noteRanges.get(note.id)?.getBoundingClientRect().right ?? window.innerWidth / 2
  const y = point?.y ?? noteRanges.get(note.id)?.getBoundingClientRect().top ?? 90
  const right = x + 16 + width <= window.innerWidth - 12 ? x + 16 : x - width - 16
  openWindows.value.push({ id: note.id, x: clamp(right + offset, 12, window.innerWidth - width - 12),
    y: clamp(y + offset, 12, window.innerHeight - height - 12), width, height, z: ++topWindow })
  selectionAction.value = undefined
}
function closeWindow(id: string) { openWindows.value = openWindows.value.filter(window => window.id !== id) }
function startWindowGesture(event: PointerEvent, id: string, kind: 'move' | 'resize') {
  if (event.button !== 0 || (kind === 'move' && (event.target as HTMLElement).closest('button'))) return
  const item = openWindows.value.find(window => window.id === id)
  if (!item) return
  event.preventDefault()
  focusWindow(id)
  windowGesture = { id, kind, x: event.clientX, y: event.clientY, original: { ...item } }
  window.addEventListener('pointermove', moveWindowGesture)
  window.addEventListener('pointerup', endWindowGesture, { once: true })
  window.addEventListener('pointercancel', endWindowGesture, { once: true })
}
function moveWindowGesture(event: PointerEvent) {
  if (!windowGesture) return
  const item = openWindows.value.find(window => window.id === windowGesture!.id)
  if (!item) return
  const { original, kind, x, y } = windowGesture
  if (kind === 'move') {
    item.x = clamp(original.x + event.clientX - x, 12, window.innerWidth - item.width - 12)
    item.y = clamp(original.y + event.clientY - y, 12, window.innerHeight - item.height - 12)
  } else {
    item.width = clamp(original.width + event.clientX - x, Math.min(300, window.innerWidth - 24), window.innerWidth - item.x - 12)
    item.height = clamp(original.height + event.clientY - y, Math.min(240, window.innerHeight - 24), window.innerHeight - item.y - 12)
  }
}
function endWindowGesture() {
  windowGesture = undefined
  window.removeEventListener('pointermove', moveWindowGesture)
  window.removeEventListener('pointerup', endWindowGesture)
  window.removeEventListener('pointercancel', endWindowGesture)
}
function resizeWindowWithKeyboard(event: KeyboardEvent, id: string) {
  const item = openWindows.value.find(window => window.id === id)
  if (!item) return
  const x = event.key === 'ArrowRight' ? 24 : event.key === 'ArrowLeft' ? -24 : 0
  const y = event.key === 'ArrowDown' ? 24 : event.key === 'ArrowUp' ? -24 : 0
  if (!x && !y) return
  event.preventDefault()
  focusWindow(id)
  item.width = clamp(item.width + x, Math.min(300, window.innerWidth - 24), window.innerWidth - item.x - 12)
  item.height = clamp(item.height + y, Math.min(240, window.innerHeight - 24), window.innerHeight - item.y - 12)
}
function clampWindows() {
  for (const item of openWindows.value) {
    item.width = Math.min(item.width, window.innerWidth - 24)
    item.height = Math.min(item.height, window.innerHeight - 24)
    item.x = clamp(item.x, 12, window.innerWidth - item.width - 12)
    item.y = clamp(item.y, 12, window.innerHeight - item.height - 12)
  }
}
window.addEventListener('resize', clampWindows)
function textRange(root: Element, start: number, end: number): Range | undefined {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  const result = document.createRange()
  let offset = 0
  let startFound = false
  while (walker.nextNode()) {
    const node = walker.currentNode
    const length = node.textContent?.length || 0
    if (!startFound && start <= offset + length) { result.setStart(node, Math.max(0, start - offset)); startFound = true }
    if (startFound && end <= offset + length) { result.setEnd(node, Math.max(0, end - offset)); return result }
    offset += length
  }
}
function updateMarkers() {
  const ranges: Range[] = []
  noteRanges.clear()
  for (const note of [...followups.value].sort((a, b) => a.messageIndex - b.messageIndex || a.start - b.start)) {
    const article = scroll.value?.querySelector<HTMLElement>(`[data-message-index="${note.messageIndex}"]`)
    const markdown = article?.querySelector(`.markdown[data-part-index="${note.partIndex}"]`)
    if (!article || !markdown) continue
    const range = textRange(markdown, note.start, note.end)
    if (!range || range.toString().trim() !== note.quote) continue
    noteRanges.set(note.id, range)
    ranges.push(range)
  }
  if ('highlights' in CSS && 'Highlight' in window) {
    if (ranges.length) CSS.highlights.set('inline-followups', new Highlight(...ranges))
    else CSS.highlights.delete('inline-followups')
  }
}
function highlightedNoteAt(x: number, y: number) {
  const caret = document.caretPositionFromPoint?.(x, y)
  const legacy = !caret ? document.caretRangeFromPoint?.(x, y) : undefined
  const node = caret?.offsetNode || legacy?.startContainer
  const offset = caret?.offset ?? legacy?.startOffset
  if (!node || offset === undefined) return
  for (const note of followups.value) {
    const range = noteRanges.get(note.id)
    if (range && [...range.getClientRects()].some(rect => x >= rect.left && x <= rect.right && y >= rect.top && y <= rect.bottom)
      && range.isPointInRange(node, offset)) return note
  }
}
function clickHighlighted(event: MouseEvent) {
  if (window.getSelection()?.toString()) return
  const note = highlightedNoteAt(event.clientX, event.clientY)
  if (!note) return
  event.preventDefault()
  openFollowup(note, { x: event.clientX, y: event.clientY })
}
function hoverHighlighted(event: MouseEvent) {
  if (scroll.value) scroll.value.style.cursor = highlightedNoteAt(event.clientX, event.clientY) ? 'pointer' : ''
}
watch(() => [sessionId.value, messages.value.length, ...followups.value.map(note => `${note.id}:${note.start}:${note.end}`)].join('|'), () => { void nextTick(updateMarkers) }, { flush: 'post' })
// A directory chosen in the sidebar starts one new conversation in that
// Workspace. Ordinary new conversations always reset to the server default.
watch(() => [sessionId.value, route.query.workspace] as const, ([id, value]) => {
  workspace.value = !id && typeof value === 'string' ? value : ''
}, { immediate: true })
function onScroll() { if (scroll.value) following.value = scroll.value.scrollHeight - scroll.value.scrollTop - scroll.value.clientHeight < 100 }
function scrollToBottom(behavior: ScrollBehavior = 'auto') {
  const element = scroll.value
  if (!element) return
  const max = Math.max(0, element.scrollHeight - element.clientHeight)
  if (Math.abs(element.scrollTop - max) <= 1) return
  element.scrollTo({ top: max, behavior })
}
async function latest() { following.value = true; await nextTick(); scrollToBottom('smooth') }
async function keepFollowingLatest() {
  if (!following.value) return
  await nextTick()
  scrollToBottom()
}
watch(() => [transcript.value.length, ...liveTurns.value.map(turn => turn.output.length + turn.reasoning.length + (turn.blocks || []).filter(block => block.kind !== 'tool').length), requests.value.length].join('|'), keepFollowingLatest, { flush: 'post' })
watch(() => liveTurns.value.map(turn => (turn.blocks || []).filter(block => block.kind === 'tool').length).join('|'), () => {
  if (showToolCalls.value) void keepFollowingLatest()
}, { flush: 'post' })
watch(() => toolCalls.value.length, () => {
  if (showToolCalls.value) void keepFollowingLatest()
}, { flush: 'post' })
async function toggleToolCalls() {
  const container = scroll.value
  const wasFollowing = following.value
  const anchor = container ? { top: container.scrollTop } : undefined
  showToolCalls.value = !showToolCalls.value
  await nextTick()
  if (!container || !anchor) return
  container.scrollTop = wasFollowing ? Math.max(0, container.scrollHeight - container.clientHeight) : anchor.top
}
async function send(input: string, attachments: Attachment[], done: () => void) {
  sending.value = true
  try {
    let id = sessionId.value
    if (!id) {
      const root = workspace.value.trim()
      const title = titleForFirstMessage(input) || t('newChat')
      const item = await runtime.createSession(title, root)
      id = item.id
      await router.push('/sessions/' + encodeURIComponent(id))
    }
    await runtime.send(id, input, attachments)
    done()
    await latest()
  } catch (error) { runtime.notify(error instanceof APIError ? error.message : t('unknownSend') + ' ' + errorMessage(error)) }
  finally { sending.value = false }
}
async function copy(text: string) {
  if (!text) return
  try { await navigator.clipboard.writeText(text); runtime.notify(t('copied'), 'info') }
  catch (error) { runtime.notify(errorMessage(error)) }
}
async function restore() {
  try { await runtime.mutateSession(sessionId.value, 'restore') } catch (error) { runtime.notify(errorMessage(error)) }
}
onBeforeUnmount(() => { media.removeEventListener('change', resize); window.removeEventListener('resize', clampWindows); endWindowGesture(); composerObserver?.disconnect(); if ('highlights' in CSS) CSS.highlights.delete('inline-followups'); runtime.activeSession = '' })
</script>
<template>
  <div class="chat-layout" :class="{ 'with-details': details && !narrow }">
    <section ref="chatMain" class="chat-main">
      <WorkspaceHeader class="conversation-header" @navigation="$emit('navigation')" @pending="$emit('pending')">
        <h1 class="truncate" :title="session?.title || t('newChat')">{{ session?.title || t('newChat') }}</h1><span v-if="session?.archivedAt" class="muted text-xs shrink-0">{{ t('archived') }}</span><div v-if="session?.workspace" class="muted text-xs truncate" :title="session.workspace">{{ t('workspace') }}: {{ session.workspace }}</div>
        <template #actions><button type="button" class="icon-button tool-calls-toggle" :class="{ accent: showToolCalls }" :aria-label="t(showToolCalls ? 'hideToolCalls' : 'showToolCalls')" :aria-pressed="showToolCalls" :title="t(showToolCalls ? 'hideToolCalls' : 'showToolCalls')" @click="toggleToolCalls"><Terminal :size="18" /></button><button type="button" class="icon-button" :class="{ accent: details }" :aria-label="t('execution')" :aria-expanded="details" @click="details = !details"><Activity :size="18" /></button><SessionMenu v-if="session" :session="session" /></template>
      </WorkspaceHeader>
      <div ref="scroll" class="conversation-scroll" @scroll.passive="onScroll">
        <div v-if="welcome" class="welcome">
          <Brand large />
          <div class="welcome-eyebrow">{{ t('welcomeNote') }}</div>
          <h2>{{ t('welcome') }}</h2>
          <p>{{ t('welcomeSub') }}</p>
        </div>
        <div v-else class="transcript" @mouseup="captureSelection" @keyup="captureSelection" @click="clickHighlighted" @mousemove="hoverHighlighted" @mouseleave="scroll && (scroll.style.cursor = '')">
          <div v-if="!session && runtime.connection === 'online'" class="empty-panel"><p>{{ t('missingSession') }}</p><RouterLink to="/new" class="btn">{{ t('back') }}</RouterLink></div>
          <div v-if="runtime.historyLoading[sessionId] && !messages.length" class="history-loading"><LoaderCircle class="spin" :size="16" /><div>{{ t('loadingHistory') }}<p v-if="running.length" class="muted text-xs mt-1">{{ t('historyWaiting') }}</p></div></div>
          <div v-if="runtime.historyErrors[sessionId]" class="error-banner"><p>{{ runtime.historyErrors[sessionId] }}</p><button class="text-button" @click="runtime.loadHistory(sessionId)">{{ t('retry') }}</button></div>
          <template v-for="entry in transcript" :key="entry.id">
            <article v-if="entry.kind === 'message'" class="message" :class="'message-' + entry.message.role" :data-message-index="entry.message.role === 'assistant' ? entry.index : undefined">
              <div v-if="entry.message.role !== 'user'" class="message-byline"><Brand /><span>{{ entry.message.role === 'assistant' ? t('assistant') : entry.message.role }}</span></div>
              <div class="message-content"><ContentParts :parts="entry.message.content" /></div>
              <ToolCard v-for="call in entry.message.toolCalls" :key="call.id" :name="call.name" :arguments="call.arguments" :content="toolResults.get(call.id)?.content" />
              <button v-if="entry.message.role === 'assistant' && turnCopies.has(entry.index)" class="icon-button message-copy" :aria-label="t('copy')" @click="copy(turnCopies.get(entry.index)!)"><Copy :size="14" /></button>
            </article>
            <article v-else class="message message-assistant live-message">
              <template v-if="showTurn(entry.turn)">
                <div v-if="shouldShowTurnByline(entry.turn, showToolCalls, runtime.interactions)" class="message-byline"><Brand /><span>Ingot</span><StatusBadge :status="entry.turn.status" /></div>
                <TurnContent :turn="entry.turn" :interactions="runtime.interactions" :historical-tool-ids="historicalToolIds" :show-tool-calls="showToolCalls" />
              </template>
              <p v-if="entry.turn.error" class="error-banner">{{ entry.turn.error.message }}</p>
              <button v-if="entry.turn.status !== 'running'" class="execution-link" @click="details = true"><Activity :size="13" /><span>{{ t('status.' + entry.turn.status) }}</span><template v-if="entry.turn.outcome"><span>·</span><span>{{ (entry.turn.outcome.durationNs / 1e9).toFixed(1) }}s</span></template><ChevronRight :size="12" /></button>
            </article>
          </template>
          <template v-if="showToolCalls"><ToolCard v-for="call in toolCalls" :key="call.id" :name="call.name" :arguments="call.arguments" :content="call.content" :status="call.status" :error="call.error" /></template>
          <InteractionCard v-for="item in looseRequests" :key="item.id" :interaction="item" />
          <details v-for="state in hostStates" :key="state.id" class="host-state"><summary>{{ state.description || state.name }}</summary><JsonBlock :value="state.values" /></details>
        </div>
      </div>
      <button v-if="selectionAction" type="button" class="followup-selection btn small" :style="selectionPosition(selectionAction.x, selectionAction.y)" :disabled="openingFollowup" @mousedown.prevent @click="openDraft"><LoaderCircle v-if="openingFollowup" class="spin" :size="15" /><MessageCircleQuestion v-else :size="15" />{{ t('followup') }}</button>
      <div ref="composerDock" class="composer-dock" :class="{ 'welcome-composer': welcome }">
        <div v-if="welcome" class="workspace-picker">
          <button type="button" class="workspace-browse" :aria-label="t('chooseWorkspace')" :disabled="selectingWorkspace" @click="chooseWorkspace">
            <LoaderCircle v-if="selectingWorkspace" class="spin" :size="16" /><FolderOpen v-else :size="16" /><span>{{ t(selectingWorkspace ? 'workspaceSelecting' : 'chooseWorkspace') }}</span>
          </button>
          <span v-if="effectiveWorkspace" class="workspace-chosen truncate" :title="effectiveWorkspace">{{ effectiveWorkspace }}</span>
          <span class="muted text-xs shrink-0">{{ t(workspace ? 'workspaceHint' : 'defaultWorkspace') }}</span>
        </div>
        <button v-if="!following && !welcome" class="latest-button" @click="latest"><ArrowDown :size="14" />{{ t('showLatest') }}</button>
        <div v-if="needsWorkspace" class="workspace-assignment-banner">
          <div><strong>{{ t('workspaceDefaultTitle') }}</strong><p>{{ t('workspaceDefaultExisting', { path: runtime.defaultWorkspace }) }}</p></div>
          <button type="button" class="btn small" :disabled="selectingWorkspace || assigningWorkspace" @click="chooseWorkspace"><LoaderCircle v-if="selectingWorkspace || assigningWorkspace" class="spin" :size="14" /><FolderOpen v-else :size="14" />{{ t(assigningWorkspace ? 'workspaceAssigning' : selectingWorkspace ? 'workspaceSelecting' : 'chooseWorkspace') }}</button>
        </div>
        <div v-if="session?.archivedAt" class="archive-banner"><span>{{ t('archivedSession') }}</span><button class="btn small" @click="restore">{{ t('restore') }}</button></div>
        <Composer :session-key="sessionId || 'new'" :running="running" :archived="!!session?.archivedAt || (!!sessionId && !session)" :disabled="selectingWorkspace || assigningWorkspace" :sending="sending" @send="send" @command="$emit('operation', $event, sessionId)" />
      </div>
    </section>
    <div class="followup-layer">
      <div v-for="item in visibleWindows" :key="item.note.id" class="followup-window" :data-note-id="item.note.id" :style="{ left: item.window.x + 'px', top: item.window.y + 'px', width: item.window.width + 'px', height: item.window.height + 'px', zIndex: item.window.z }" @pointerdown="focusWindow(item.note.id)">
        <FollowupNote :source-session-id="sessionId" :anchor="item.note" :note="item.note" @drag-start="startWindowGesture($event, item.note.id, 'move')" @deleted="closeWindow(item.note.id)" @close="closeWindow(item.note.id)" />
        <button type="button" class="followup-resize" :aria-label="t('resizeFollowupWindow')" :title="t('resizeFollowupWindow')" @pointerdown.stop="startWindowGesture($event, item.note.id, 'resize')" @keydown="resizeWindowWithKeyboard($event, item.note.id)"><Grip :size="13" /></button>
      </div>
    </div>
    <aside v-if="details && !narrow" class="details-sidebar"><header><h2>{{ t('execution') }}</h2><button class="icon-button" :aria-label="t('close')" @click="details = false"><X :size="17" /></button></header><ExecutionPanel :session-id="sessionId" /></aside>
    <Overlay :open="details && narrow" :title="t('execution')" drawer @update:open="details = $event"><ExecutionPanel :session-id="sessionId" /></Overlay>
  </div>
</template>

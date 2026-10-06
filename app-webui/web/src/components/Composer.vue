<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ArrowUp, Paperclip, Square, X, File, LoaderCircle } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { Attachment, LiveTurn, Operation } from '../protocol'
import { command, errorMessage } from '../api'
import { useRuntime } from '../stores/runtime'
import { filterGroups, filterOperations, operationGroups, parseComposerInput } from '../commands'
import CommandPalette from './CommandPalette.vue'
import ModelSelectionPicker from './ModelSelectionPicker.vue'
const props = defineProps<{ sessionKey: string; running: LiveTurn[]; archived?: boolean; disabled?: boolean; sending?: boolean }>()
const emit = defineEmits<{
  send: [input: string, attachments: Attachment[], done: () => void]
  command: [operation: Operation]
}>()
const runtime = useRuntime()
const { t } = useI18n()
const text = ref('')
const input = ref<HTMLTextAreaElement>()
const selecting = ref(false)
let selection: AbortController | undefined
const paletteDismissed = ref(false)
const selectedCommand = ref(0)
interface Upload { id: number; attachment: Attachment; preview?: string }
const uploads = ref<Upload[]>([])
const drafts = new Map<string, { text: string; uploads: Upload[] }>()
let nextId = 0
const parsed = computed(() => parseComposerInput(text.value, runtime.operations))
const groups = computed(() => operationGroups(runtime.operations))
const groupCandidates = computed(() => parsed.value.kind === 'group-search' ? filterGroups(groups.value, parsed.value.query) : [])
const operationCandidates = computed(() => parsed.value.kind === 'operation-search' ? filterOperations(parsed.value.group.operations, parsed.value.query) : [])
const paletteOpen = computed(() => !paletteDismissed.value && (parsed.value.kind === 'group-search' || parsed.value.kind === 'operation-search'))
const commandReady = computed(() => parsed.value.kind === 'command' && uploads.value.length === 0)
const canSend = computed(() => runtime.connection === 'online' && !props.sending && !selecting.value && !props.archived && !props.disabled &&
  !props.running.length && parsed.value.kind === 'message' && (!!parsed.value.text.trim() || uploads.value.length > 0))
const canSubmit = computed(() => canSend.value || (runtime.connection === 'online' && !props.sending && !props.archived && commandReady.value))
function fit() { if (input.value) { input.value.style.height = 'auto'; input.value.style.height = Math.min(input.value.scrollHeight, 200) + 'px' } }
watch(() => props.sessionKey, (key, previous) => {
  drafts.set(previous, { text: text.value, uploads: uploads.value })
  if (previous === 'new' && props.sending && !drafts.has(key)) {
    drafts.set(key, drafts.get(previous)!)
    drafts.delete(previous)
  }
  const draft = drafts.get(key)
  text.value = draft?.text || ''
  uploads.value = draft?.uploads || []
})
watch(text, () => { paletteDismissed.value = false; selectedCommand.value = 0; fit() }, { flush: 'post' })
const size = (bytes: number) => bytes < 1048576 ? Math.ceil(bytes / 1024) + ' KB' : (bytes / 1048576).toFixed(1) + ' MB'
function release(item: Upload) { if (item.preview?.startsWith('blob:')) URL.revokeObjectURL(item.preview) }
function remove(id: number) {
  const item = uploads.value.find(item => item.id === id)
  if (item) release(item)
  uploads.value = uploads.value.filter(item => item.id !== id)
}
async function selectFiles() {
  if (!runtime.files.available || selecting.value || props.archived || props.disabled || props.sending) return
  selecting.value = true
  const controller = new AbortController()
  selection = controller
  const target = uploads.value
  const initialPath = runtime.sessions.find(item => item.id === props.sessionKey)?.workspace || runtime.defaultWorkspace
  try {
    const result = await command<{ files: Attachment[] }>('/files/select', 'POST', { initialPath }, controller.signal)
    if (controller.signal.aborted) return
    for (const attachment of result.files) {
      target.push({ id: ++nextId, attachment, preview: attachment.kind === 'image' && attachment.assetId ? '/api/assets/' + encodeURIComponent(attachment.assetId) : undefined })
    }
  } catch (error) {
    if (!controller.signal.aborted) runtime.notify(errorMessage(error))
  } finally {
    selecting.value = false
    selection = undefined
  }
}
function send() {
  if (parsed.value.kind === 'command') { executeCommand(parsed.value.operation); return }
  if (parsed.value.kind !== 'message' || !canSend.value) return
  const sentList = uploads.value
  const sentUploads = [...uploads.value]
  emit('send', parsed.value.text, sentUploads.map(item => item.attachment), () => {
    for (const item of sentUploads) release(item)
    for (const [key, draft] of drafts) if (draft.uploads === sentList) drafts.delete(key)
    if (uploads.value === sentList) { text.value = ''; uploads.value = [] }
  })
}
function chooseGroup(group = groupCandidates.value[selectedCommand.value]) {
  if (!group) return
  text.value = '/' + group.name + ' '
}
function completeOperation(operation = operationCandidates.value[selectedCommand.value]) {
  if (!operation) return
  text.value = '/' + operation.group + ' ' + operation.name
}
function executeCommand(operation: Operation) {
  if (uploads.value.length) { runtime.notify(t('commandAttachments'), 'error'); return }
  if (runtime.connection !== 'online' || props.sending || props.archived) return
  emit('command', operation)
  text.value = ''
}
function invalidCommand() {
  const value = parsed.value
  if (value.kind === 'group-search') runtime.notify(t('unknownCommandGroup', { group: value.query || '/' }), 'error')
  else if (value.kind === 'invalid-command') {
    runtime.notify(t(value.reason === 'unknown-group' ? 'unknownCommandGroup' : value.reason === 'trailing-input' ? 'commandTrailingInput' : 'unknownCommandOperation', {
      group: value.group, operation: value.operation,
    }), 'error')
  }
}
function keydown(event: KeyboardEvent) {
  if (event.isComposing || event.keyCode === 229) return
  const candidates = parsed.value.kind === 'group-search' ? groupCandidates.value : parsed.value.kind === 'operation-search' ? operationCandidates.value : []
  if (event.key === 'Escape' && paletteOpen.value) { event.preventDefault(); paletteDismissed.value = true; return }
  if (paletteOpen.value && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) {
    event.preventDefault()
    if (candidates.length) selectedCommand.value = (selectedCommand.value + (event.key === 'ArrowDown' ? 1 : -1) + candidates.length) % candidates.length
    return
  }
  if (event.key === 'Tab' && paletteOpen.value && candidates.length) {
    event.preventDefault()
    if (parsed.value.kind === 'group-search') chooseGroup()
    else completeOperation()
    return
  }
  if (event.key !== 'Enter' || event.shiftKey) return
  event.preventDefault()
  if (parsed.value.kind === 'group-search') { if (candidates.length) chooseGroup(); else invalidCommand(); return }
  if (parsed.value.kind === 'operation-search') { if (operationCandidates.value.length) executeCommand(operationCandidates.value[selectedCommand.value]); else invalidCommand(); return }
  if (parsed.value.kind === 'invalid-command') { invalidCommand(); return }
  send()
}
async function stop() {
  for (const turn of props.running) {
    try { await runtime.stop(turn) } catch (error) { runtime.notify(errorMessage(error)) }
  }
}
onBeforeUnmount(() => {
  selection?.abort()
  for (const draft of drafts.values()) for (const item of draft.uploads) release(item)
  for (const item of uploads.value) release(item)
})
</script>
<template>
  <div class="composer-wrap">
    <CommandPalette v-if="paletteOpen && parsed.kind === 'group-search'" phase="group" :groups="groupCandidates" :selected="selectedCommand" @group="chooseGroup" />
    <CommandPalette v-if="paletteOpen && parsed.kind === 'operation-search'" phase="operation" :group="parsed.group.name" :operations="operationCandidates" :selected="selectedCommand" @operation="executeCommand" />
    <form class="composer" @submit.prevent="send">
      <div v-if="uploads.length" class="upload-list">
        <div v-for="item in uploads" :key="item.id" class="upload-chip" :title="item.attachment.path">
          <img v-if="item.preview" :src="item.preview" alt="" /><File v-else :size="20" class="muted" />
          <span class="min-w-0"><strong class="block truncate">{{ item.attachment.name }}</strong><small>{{ size(item.attachment.size || 0) }}</small></span>
          <button type="button" class="icon-button" :aria-label="t('remove') + ': ' + item.attachment.name" :disabled="sending" @click="remove(item.id)"><X :size="13" /></button>
        </div>
      </div>
      <textarea ref="input" v-model="text" class="composer-input" :placeholder="t('composer')" :aria-label="t('composer')" rows="2" :disabled="archived || sending" @keydown="keydown" />
      <div class="composer-toolbar">
        <button type="button" class="icon-button" :disabled="!runtime.files.available || selecting || archived || disabled || sending" :aria-label="t('attach')" :title="runtime.files.available ? t('uploadLimit', { size: size(runtime.files.maxBytes) }) : t('assetUnavailable')" @click="selectFiles"><LoaderCircle v-if="selecting" class="spin" :size="19" /><Paperclip v-else :size="19" /></button>
        <ModelSelectionPicker />
        <button v-if="running.length" type="button" class="send-button stopping-button ml-auto" :disabled="running.every(turn => turn.stopping) || runtime.connection !== 'online'" :aria-label="t(running.some(turn => turn.stopping) ? 'stopping' : 'stop')" @click="stop"><LoaderCircle v-if="running.some(turn => turn.stopping)" class="spin" :size="18" /><Square v-else :size="14" fill="currentColor" /></button>
        <button v-else class="send-button ml-auto" type="submit" :disabled="!canSubmit" :aria-label="t(parsed.kind === 'command' ? 'runOperation' : 'send')"><LoaderCircle v-if="sending" class="spin" :size="18" /><ArrowUp v-else :size="20" /></button>
      </div>
    </form>
    <div class="composer-hint">{{ t('shortcut') }}</div>
  </div>
</template>

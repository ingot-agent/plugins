<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, provide, ref, watch } from 'vue'
import { DialogContent, DialogPortal, DialogRoot, DialogTitle } from 'reka-ui'
import { ArrowLeft, Copy, ExternalLink, FileText, LoaderCircle, Maximize2, Minimize2, RefreshCw, X } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import hljs from 'highlight.js/lib/common'
import { APIError, command, errorMessage, isAbort } from '../api'
import { fileContextKey, type FilePreview, type FilePreviewRequest } from '../filePreview'
import MarkdownContent from './MarkdownContent.vue'

const props = defineProps<{ request: FilePreviewRequest; overlay?: boolean }>()
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()
const title = ref<HTMLElement>()
const panel = ref<HTMLElement>()
const scroller = ref<HTMLElement>()
const file = ref<FilePreview>()
const loading = ref(false)
const error = ref('')
const actionError = ref('')
const opening = ref(false)
const copied = ref(false)
const source = ref(false)
const expanded = ref(false)
const width = ref(520)
const media = window.matchMedia('(max-width: 1199px)')
const mobile = ref(media.matches)
let controller: AbortController | undefined
let copyTimer: ReturnType<typeof setTimeout> | undefined
let drag: { x: number; width: number } | undefined
const modal = computed(() => mobile.value || expanded.value || Boolean(props.overlay))
const path = computed(() => file.value?.path || props.request.path)
const name = computed(() => file.value?.name || props.request.path.split(/[\\/]/).pop() || props.request.path)
const markdown = computed(() => /\.(md|markdown|mdown)$/i.test(name.value))
const lineCount = computed(() => file.value?.text.split('\n').length || 1)
const line = computed(() => props.request.line ? Math.min(props.request.line, lineCount.value) : undefined)
const lastLine = computed(() => Math.min(props.request.endLine || line.value || 1, lineCount.value))
const numbers = computed(() => Array.from({ length: lineCount.value }, (_, index) => index + 1).join('\n'))
const showSource = computed(() => source.value || !markdown.value)
const language = computed(() => {
  const extension = name.value.split('.').pop()?.toLowerCase() || ''
  const aliases: Record<string, string> = { vue: 'xml', html: 'xml', htm: 'xml', svg: 'xml', jsx: 'javascript', tsx: 'typescript', mjs: 'javascript', cjs: 'javascript', yml: 'yaml', sh: 'bash', zsh: 'bash', py: 'python', rs: 'rust', md: 'markdown', toml: 'ini', go: 'go' }
  const candidate = aliases[extension] || extension
  return hljs.getLanguage(candidate) ? candidate : ''
})
const highlighted = computed(() => {
  const text = file.value?.text || ''
  if (language.value && text.length <= 200000) return hljs.highlight(text, { language: language.value, ignoreIllegals: true }).value
  return text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
})
provide(fileContextKey, computed(() => ({ sessionId: props.request.sessionId, basePath: file.value?.path })))

function input() {
  return { path: path.value, sessionId: props.request.sessionId, basePath: props.request.basePath }
}
function friendlyError(cause: unknown) {
  if (cause instanceof APIError) {
    if (cause.detail.code === 'file_open_failed') return t('fileOpenFailed', { message: cause.detail.message })
    const messages: Record<string, string> = {
      file_not_found: 'fileNotFound', file_permission_denied: 'filePermissionDenied',
      preview_too_large: 'fileTooLarge', preview_not_text: 'fileNotText',
      file_open_unavailable: 'fileOpenUnavailable',
    }
    const key = messages[cause.detail.code]
    if (key) return t(key)
  }
  return errorMessage(cause)
}
async function load() {
  controller?.abort()
  const pending = new AbortController()
  controller = pending
  loading.value = true
  error.value = ''
  actionError.value = ''
  file.value = undefined
  copied.value = false
  try {
    const result = await command<FilePreview>('/files/preview', 'POST', input(), pending.signal)
    if (!pending.signal.aborted) file.value = result
  } catch (cause) {
    if (!pending.signal.aborted && !isAbort(cause)) error.value = friendlyError(cause)
  } finally {
    if (controller === pending) loading.value = false
  }
}
async function openLocal() {
  if (opening.value) return
  opening.value = true
  actionError.value = ''
  const request = props.request
  try { await command('/files/open', 'POST', input()) }
  catch (cause) { if (props.request === request) actionError.value = friendlyError(cause) }
  finally { opening.value = false }
}
async function copyPath() {
  try {
    await navigator.clipboard.writeText(path.value)
    copied.value = true
    clearTimeout(copyTimer)
    copyTimer = setTimeout(() => { copied.value = false }, 2000)
  } catch (cause) { actionError.value = errorMessage(cause) }
}
async function locate() {
  await nextTick()
  const element = scroller.value
  if (!element || !file.value) return
  element.scrollTop = 0
  element.scrollLeft = 0
  if (showSource.value && line.value) {
    element.scrollTop = Math.max(0, 16 + (line.value - 1) * 22 - element.clientHeight / 3)
    if (props.request.column) element.scrollLeft = Math.max(0, (props.request.column - 1) * 7.8 - element.clientWidth / 2)
  } else if (!showSource.value && props.request.fragment) {
    const slug = (text: string) => text.trim().toLowerCase().replace(/[^\p{L}\p{N}\s_-]/gu, '').replace(/\s+/g, '-')
    const heading = Array.from(element.querySelectorAll<HTMLElement>('h1, h2, h3, h4, h5, h6'))
      .find(item => slug(item.textContent || '') === props.request.fragment?.toLowerCase())
    if (heading) element.scrollTop = heading.getBoundingClientRect().top - element.getBoundingClientRect().top - 16
  }
}
function clampWidth(value: number) {
  const available = panel.value?.parentElement?.clientWidth || window.innerWidth
  return Math.max(320, Math.min(value, available - 360))
}
function move(event: PointerEvent) {
  if (drag) width.value = clampWidth(drag.width + drag.x - event.clientX)
}
function endDrag() {
  drag = undefined
  window.removeEventListener('pointermove', move)
  window.removeEventListener('pointerup', endDrag)
  window.removeEventListener('pointercancel', endDrag)
}
function startDrag(event: PointerEvent) {
  if (event.button !== 0) return
  event.preventDefault()
  drag = { x: event.clientX, width: panel.value?.clientWidth || width.value }
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', endDrag)
  window.addEventListener('pointercancel', endDrag)
}
function resizeKey(event: KeyboardEvent) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
  event.preventDefault()
  width.value = clampWidth(width.value + (event.key === 'ArrowLeft' ? 32 : -32))
}
function resized() { mobile.value = media.matches; width.value = clampWidth(width.value) }
function focusPanel(event: Event) { event.preventDefault(); title.value?.focus({ preventScroll: true }) }
watch(() => props.request, () => {
  source.value = Boolean(props.request.line)
  void load()
}, { immediate: true })
watch([file, showSource], () => { void locate() }, { flush: 'post' })
watch(modal, async () => {
  const top = scroller.value?.scrollTop || 0
  const left = scroller.value?.scrollLeft || 0
  await nextTick()
  if (scroller.value) { scroller.value.scrollTop = top; scroller.value.scrollLeft = left }
})
onMounted(() => { resized(); window.addEventListener('resize', resized) })
onBeforeUnmount(() => { controller?.abort(); clearTimeout(copyTimer); endDrag(); window.removeEventListener('resize', resized) })
</script>

<template>
  <DialogRoot :open="true" :modal="modal" @update:open="value => { if (!value) emit('close') }">
    <DialogPortal :disabled="!modal">
      <DialogContent as-child :aria-describedby="undefined" @open-auto-focus="focusPanel" @close-auto-focus.prevent @interact-outside.prevent @escape-key-down.prevent>
    <aside ref="panel" class="file-preview" :class="{ expanded: expanded || overlay, mobile }" :style="{ '--preview-width': width + 'px' }" :role="modal ? 'dialog' : 'region'" :aria-modal="modal || undefined" @keydown.esc.stop.prevent="emit('close')">
      <div v-if="!modal" class="file-preview-resize" role="separator" tabindex="0" aria-orientation="vertical" :aria-label="t('resizeFilePreview')" :aria-valuenow="width" :aria-valuemin="320" @pointerdown="startDrag" @keydown="resizeKey" />
      <header class="file-preview-header">
        <div class="file-preview-title">
          <FileText :size="18" class="muted shrink-0" />
          <div class="min-w-0 flex-1"><DialogTitle as-child><h2 ref="title" tabindex="-1">{{ name }}</h2></DialogTitle><span>{{ t('filePreview') }}</span></div>
          <button v-if="!mobile && !overlay" type="button" class="icon-button" :title="t(expanded ? 'collapseFilePreview' : 'expandFilePreview')" :aria-label="t(expanded ? 'collapseFilePreview' : 'expandFilePreview')" @click="expanded = !expanded"><Minimize2 v-if="expanded" :size="16" /><Maximize2 v-else :size="16" /></button>
          <button type="button" class="icon-button" :aria-label="t('closeFilePreview')" @click="emit('close')"><X :size="18" /></button>
        </div>
        <div class="file-preview-path"><span :title="path">{{ path }}</span><button type="button" class="icon-button" :title="t(copied ? 'copied' : 'copyPath')" :aria-label="t(copied ? 'copied' : 'copyPath')" @click="copyPath"><Copy :size="14" /></button></div>
        <div class="file-preview-toolbar">
          <div v-if="file && markdown" class="file-preview-modes" :aria-label="t('fileViewMode')">
            <button type="button" :class="{ selected: !source }" :aria-pressed="!source" @click="source = false">{{ t('preview') }}</button>
            <button type="button" :class="{ selected: source }" :aria-pressed="source" @click="source = true">{{ t('fileSource') }}</button>
          </div>
          <button type="button" class="icon-button" :disabled="loading" :title="t('refreshFile')" :aria-label="t('refreshFile')" @click="load"><RefreshCw :size="14" /></button>
          <button type="button" class="btn small ml-auto" :disabled="opening" :title="t('openLocalFileHint')" @click="openLocal"><LoaderCircle v-if="opening" :size="14" class="spin" /><ExternalLink v-else :size="14" />{{ t('openLocalFile') }}</button>
        </div>
      </header>
      <p v-if="actionError" class="file-preview-error error-text" role="alert">{{ actionError }}</p>
      <div v-if="loading" class="file-preview-empty" role="status"><LoaderCircle :size="22" class="spin muted" /><p>{{ t('loadingFile') }}</p></div>
      <div v-else-if="error" class="file-preview-empty"><FileText :size="30" class="muted" /><p role="alert">{{ error }}</p><button type="button" class="btn small" @click="load">{{ t('retry') }}</button></div>
      <div v-else-if="file" ref="scroller" class="file-preview-scroll" tabindex="0" :aria-label="t('fileContents')">
        <div v-if="showSource" class="file-source">
          <div v-if="line" class="file-line-highlight" :style="{ top: 16 + (line - 1) * 22 + 'px', height: (lastLine - line + 1) * 22 + 'px' }" />
          <pre class="file-line-numbers" aria-hidden="true">{{ numbers }}</pre>
          <!-- eslint-disable-next-line vue/no-v-html -->
          <pre class="file-source-code"><code v-html="highlighted" /></pre>
        </div>
        <div v-else class="file-document"><MarkdownContent :text="file.text" /></div>
      </div>
      <footer class="file-preview-footer">
        <span v-if="file">{{ t('fileLines', { count: lineCount }) }}<template v-if="line"> · {{ t('fileLine', { line }) }}</template></span>
        <span v-else>{{ t('filePreview') }}</span><span>{{ t('readOnly') }}</span>
        <p v-if="file && request.line && request.line > lineCount" role="status">{{ t('fileLineOutside', { line: request.line }) }}</p>
      </footer>
      <button v-if="mobile" type="button" class="file-preview-back text-button" @click="emit('close')"><ArrowLeft :size="15" />{{ t('backToConversation') }}</button>
    </aside>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>

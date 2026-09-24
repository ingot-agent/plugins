<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowUp, LoaderCircle, MessageCircleQuestion, Square, Trash2, X } from 'lucide-vue-next'
import { errorMessage } from '../api'
import { useRuntime } from '../stores/runtime'
import { readPreference, savePreference } from '../theme'
import type { Followup, FollowupAnchor, Message } from '../protocol'
import ContentParts from './ContentParts.vue'
import TurnContent from './TurnContent.vue'
import InteractionCard from './InteractionCard.vue'
import Brand from './Brand.vue'

const props = defineProps<{ sourceSessionId: string; anchor: FollowupAnchor; note?: Followup }>()
const emit = defineEmits<{ close: []; created: [item: Followup]; deleted: []; dragStart: [event: PointerEvent] }>()
const { t } = useI18n()
const runtime = useRuntime()
const question = ref('')
const input = ref<HTMLTextAreaElement>()
const sending = ref(false)
const confirmingDelete = ref(false)
const noteId = computed(() => props.note?.id || '')
const history = computed(() => (runtime.histories[noteId.value] || []).slice(props.note?.baseMessageCount || 0).filter(item => item.role !== 'tool'))
const turns = computed(() => Object.values(runtime.turns).filter(turn => turn.sessionId === noteId.value && (!turn.reconciled || turn.status !== 'succeeded')))
const running = computed(() => runtime.running(noteId.value))
const requests = computed(() => Object.values(runtime.interactions).filter(item => item.scope?.agent?.sessionId === noteId.value))
function visibleParts(message: Message, index: number) {
  if (index !== 0 || message.role !== 'user' || message.content[0]?.kind !== 'text') return message.content
  const text = message.content[0].text || ''
  const quotedAt = text.indexOf(props.anchor.quote)
  const boundary = quotedAt < 0 ? -1 : text.indexOf('\n\n', quotedAt + props.anchor.quote.length)
  return boundary < 0 ? message.content : [{ ...message.content[0], text: text.slice(boundary + 2) }, ...message.content.slice(1)]
}
watch(noteId, id => {
  question.value = id ? readPreference('followup-draft.' + id, '') : ''
  if (id) void runtime.loadHistory(id)
}, { immediate: true })
watch(question, value => { if (noteId.value) savePreference('followup-draft.' + noteId.value, value) })
function fitInput() {
  const element = input.value
  if (!element) return
  element.style.height = 'auto'
  element.style.height = `${Math.min(element.scrollHeight, 150)}px`
  element.style.overflowY = element.scrollHeight > 150 ? 'auto' : 'hidden'
}
watch(question, () => { void nextTick(fitInput) }, { flush: 'post' })
onMounted(fitInput)

async function send() {
  const input = question.value.trim()
  if (!input || sending.value || running.value.length || runtime.connection !== 'online') return
  sending.value = true
  try {
    let item = props.note
    if (!item) {
      item = await runtime.createFollowup(props.sourceSessionId, props.anchor)
      emit('created', item)
    }
    const prompt = history.value.length || (runtime.histories[item.id] || []).length > item.baseMessageCount
      ? input : t('followupContext', { quote: item.quote }) + '\n\n' + input
    await runtime.send(item.id, prompt, [])
    question.value = ''
  } catch (error) { runtime.notify(errorMessage(error)) }
  finally { sending.value = false }
}
async function remove() {
  if (!props.note || running.value.length) return
  try {
    await runtime.deleteFollowup(props.note)
    try { localStorage.removeItem('ingot.followup-draft.' + props.note.id) } catch { /* Storage can be disabled. */ }
    emit('deleted')
  }
  catch (error) { runtime.notify(errorMessage(error)) }
}
async function stop() {
  for (const turn of running.value) {
    try { await runtime.stop(turn) } catch (error) { runtime.notify(errorMessage(error)) }
  }
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) { event.preventDefault(); void send() }
}
</script>
<template>
  <section class="followup-note" role="dialog" :aria-label="t('followup')">
    <header class="followup-header" @pointerdown="emit('dragStart', $event)">
      <div class="followup-heading"><MessageCircleQuestion :size="16" /><strong>{{ t('followup') }}</strong></div>
      <div class="followup-actions">
        <button v-if="note" type="button" class="icon-button" :aria-label="t('delete')" :title="t('delete')" :disabled="!!running.length" @click="confirmingDelete = !confirmingDelete"><Trash2 :size="16" /></button>
        <button type="button" class="icon-button" :aria-label="t('close')" @click="emit('close')"><X :size="17" /></button>
      </div>
    </header>
    <div v-if="confirmingDelete" class="followup-confirm"><span>{{ t('deleteFollowupConfirm') }}</span><button class="btn small danger" @click="remove">{{ t('delete') }}</button><button class="btn small" @click="confirmingDelete = false">{{ t('cancel') }}</button></div>
    <div class="followup-thread">
      <blockquote class="followup-quote"><span aria-hidden="true">“</span><span>{{ anchor.quote }}</span><span aria-hidden="true">”</span></blockquote>
      <div v-if="note && runtime.historyLoading[note.id]" class="muted"><LoaderCircle class="spin" :size="16" /></div>
      <div v-if="note && runtime.historyErrors[note.id]" class="error-text">{{ runtime.historyErrors[note.id] }} <button class="text-button" @click="runtime.loadHistory(note.id)">{{ t('retry') }}</button></div>
      <div v-for="(message, index) in history" :key="index" class="followup-message" :class="'followup-' + message.role"><div v-if="message.role === 'assistant'" class="followup-byline"><Brand /><span>Ingot</span></div><ContentParts :parts="visibleParts(message, index)" /></div>
      <template v-for="turn in turns" :key="turn.id">
        <div v-if="runtime.optimistic[turn.id]" class="followup-message followup-user"><ContentParts :parts="visibleParts(runtime.optimistic[turn.id].message, history.length)" /></div>
        <div class="followup-message followup-assistant"><div class="followup-byline"><Brand /><span>Ingot</span></div><TurnContent :turn="turn" :interactions="runtime.interactions" :historical-tool-ids="new Set()" :show-tool-calls="true" /><p v-if="turn.error" class="error-text">{{ turn.error.message }}</p></div>
      </template>
      <InteractionCard v-for="request in requests" :key="request.id" :interaction="request" />
    </div>
    <div class="followup-compose">
      <div class="followup-compose-field">
        <textarea ref="input" v-model="question" rows="1" :placeholder="t('followupPlaceholder')" :aria-label="t('followupPlaceholder')" :disabled="runtime.connection !== 'online'" @keydown="keydown" />
        <button v-if="running.length" type="button" class="followup-send stopping-button" :aria-label="t('stop')" :title="t('stop')" @click="stop"><Square :size="15" /></button>
        <button v-else type="button" class="followup-send" :aria-label="t('send')" :title="t('send')" :disabled="!question.trim() || sending || runtime.connection !== 'online'" @click="send"><LoaderCircle v-if="sending" class="spin" :size="16" /><ArrowUp v-else :size="17" /></button>
      </div>
    </div>
  </section>
</template>

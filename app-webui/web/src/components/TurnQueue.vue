<script setup lang="ts">
import { computed, ref } from 'vue'
import { ChevronDown, ChevronRight, ListOrdered, LoaderCircle, Paperclip, Pause, Play, X } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { LiveTurn } from '../protocol'
import { errorMessage } from '../api'
import { useRuntime } from '../stores/runtime'

const props = defineProps<{ sessionId: string; archived?: boolean }>()
const runtime = useRuntime()
const { t } = useI18n()
const items = computed(() => runtime.queued(props.sessionId))
const paused = computed(() => !!runtime.turnQueues[props.sessionId]?.paused)
const expanded = ref(true)
const updating = ref(false)
const removing = ref(new Set<string>())
const unavailable = computed(() => runtime.connection !== 'online' || props.archived)

async function togglePaused() {
  updating.value = true
  try { await runtime.setQueuePaused(props.sessionId, !paused.value) }
  catch (error) { runtime.notify(errorMessage(error)) }
  finally { updating.value = false }
}
async function remove(turn: LiveTurn) {
  removing.value.add(turn.id)
  try { await runtime.removeQueued(turn) }
  catch (error) { runtime.notify(errorMessage(error)) }
  finally { removing.value.delete(turn.id) }
}
</script>

<template>
  <section v-if="items.length || paused" class="turn-queue" :aria-label="t('messageQueue')">
    <header class="turn-queue-header">
      <button type="button" class="turn-queue-heading" :aria-expanded="expanded" @click="expanded = !expanded">
        <ChevronDown v-if="expanded" :size="14" /><ChevronRight v-else :size="14" />
        <ListOrdered :size="15" /><span>{{ t('queuedMessages', { count: items.length }) }}</span>
      </button>
      <span v-if="paused" class="turn-queue-paused">{{ t('queuePaused') }}</span>
      <button type="button" class="icon-button" :title="t(paused ? 'resumeQueue' : 'pauseQueue')" :aria-label="t(paused ? 'resumeQueue' : 'pauseQueue')" :disabled="unavailable || updating || (paused && runtime.running(sessionId).some(turn => turn.stopping))" @click="togglePaused">
        <LoaderCircle v-if="updating" class="spin" :size="15" /><Play v-else-if="paused" :size="15" /><Pause v-else :size="15" />
      </button>
    </header>
    <ol v-if="expanded && items.length" class="turn-queue-list">
      <li v-for="(item, index) in items" :key="item.id" class="turn-queue-item">
        <span class="turn-queue-index">{{ index + 1 }}</span>
        <div class="turn-queue-copy">
          <p v-if="item.input" :title="item.input">{{ item.input }}</p>
          <small v-if="item.attachments?.length"><Paperclip :size="12" /><span>{{ item.attachments.map(file => file.name || file.path).join(', ') }}</span></small>
        </div>
        <button type="button" class="icon-button" :aria-label="t('removeQueuedMessage', { index: index + 1 })" :title="t('remove')" :disabled="unavailable || removing.has(item.id)" @click="remove(item)"><LoaderCircle v-if="removing.has(item.id)" class="spin" :size="14" /><X v-else :size="14" /></button>
      </li>
    </ol>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { Check, CircleCheck, CircleX, LoaderCircle, Square, SlidersHorizontal } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { Operation } from '../protocol'
import { useRuntime } from '../stores/runtime'
import { errorMessage } from '../api'
import Overlay from './Overlay.vue'
import StatusBadge from './StatusBadge.vue'
import InteractionForm from './InteractionForm.vue'
import MarkdownContent from './MarkdownContent.vue'
import JsonBlock from './JsonBlock.vue'

const props = defineProps<{
  open: boolean
  operation?: Operation
  sessionId?: string
  invocationId?: string
  launchToken: number
}>()
const emit = defineEmits<{ 'update:open': [value: boolean]; invocation: [id: string] }>()
const runtime = useRuntime()
const { t } = useI18n()
const localInvocationId = ref('')
const startError = ref('')
const canceling = ref(false)
const formId = useId()
const formState = ref({ busy: false, disabled: true })
const activeDepth = ref(0)
const invocationId = computed(() => props.invocationId || localInvocationId.value)
const invocation = computed(() => invocationId.value ? runtime.operationInvocations[invocationId.value] : undefined)
const interactions = computed(() => Object.values(runtime.interactions).filter(item => item.scope?.operation?.invocationId === invocationId.value))
const states = computed(() => Object.values(runtime.interactionStates).filter(item => item.scope?.operation?.invocationId === invocationId.value))
const command = computed(() => props.operation ? `/${props.operation.group} ${props.operation.name}` : '')

watch(() => props.launchToken, async () => {
  localInvocationId.value = props.invocationId || ''
  startError.value = ''
  canceling.value = false
  activeDepth.value = 0
  if (!props.open || !props.operation || props.invocationId) return
  try {
    const result = await runtime.invoke(props.operation.id, '{}', props.sessionId || '')
    localInvocationId.value = result.id
    emit('invocation', result.id)
  } catch (cause) { startError.value = errorMessage(cause) }
}, { immediate: true })

async function cancel() {
  if (!invocationId.value) return
  canceling.value = true
  try { await runtime.cancelOperation(invocationId.value) }
  catch (cause) { runtime.notify(errorMessage(cause)); canceling.value = false }
}
</script>

<template>
  <Overlay :open="open" :title="command || t('operation')" :description="operation?.description" @update:open="$emit('update:open', $event)">
    <div class="command-dialog">
      <div class="command-dialog-meta"><span><SlidersHorizontal :size="14" />{{ t('operation') }}</span><span v-if="interactions.length" class="command-editing"><span />{{ t('editingOperation') }}</span><StatusBadge v-else-if="invocation" :status="invocation.status" /></div>
      <div v-if="startError" class="command-state error-text"><CircleX :size="18" /><span>{{ startError }}</span></div>
      <div v-else-if="!invocationId || !invocation" class="command-state"><LoaderCircle class="spin accent" :size="18" /><span>{{ t(invocationId ? 'waitingOperationState' : 'startingOperation') }}</span></div>
      <template v-else>
        <div v-if="invocation.status === 'running' && !interactions.length" class="command-state"><LoaderCircle class="spin accent" :size="18" /><span>{{ t('waitingOperationInteraction') }}</span></div>
        <div v-for="(interaction, index) in interactions" :key="interaction.id" class="command-interaction">
          <MarkdownContent v-if="interaction.description && (index !== 0 || activeDepth === 0)" :text="interaction.description" />
          <InteractionForm :id="index === 0 ? formId : undefined" :interaction="interaction" :hide-submit="index === 0" @state="value => { if (index === 0) formState = value }" @navigate="depth => { if (index === 0) activeDepth = depth }" />
        </div>
        <details v-for="state in states" :key="state.id" class="host-state"><summary>{{ state.description || state.name }}</summary><JsonBlock :value="state.values" /></details>
        <div v-if="invocation.status === 'succeeded'" class="command-state success-text"><CircleCheck :size="18" /><span>{{ t('operationSucceeded') }}</span></div>
        <div v-else-if="invocation.status === 'failed'" class="command-state error-text"><CircleX :size="18" /><span>{{ invocation.error?.message || t('operationFailed') }}</span></div>
        <div v-else-if="invocation.status === 'canceled'" class="command-state muted"><Square :size="15" /><span>{{ t('operationCanceled') }}</span></div>
        <details v-if="invocation.result" class="command-result"><summary>{{ t('rawResult') }}</summary><JsonBlock :value="invocation.result.output" /></details>
      </template>
    </div>
    <template #footer>
      <button v-if="invocation?.status === 'running'" type="button" class="btn command-cancel" :aria-label="t('cancelOperation')" :disabled="canceling || formState.busy || runtime.connection !== 'online'" @click="cancel"><LoaderCircle v-if="canceling" class="spin" :size="14" /><Square v-else :size="12" />{{ t(canceling ? 'cancelingOperation' : 'cancel') }}</button>
      <button type="button" class="btn" @click="$emit('update:open', false)">{{ t('close') }}</button>
      <button v-if="interactions.length" type="submit" :form="formId" class="btn primary" :aria-label="t('submit')" :disabled="canceling || formState.disabled"><LoaderCircle v-if="formState.busy" class="spin" :size="15" /><Check v-else :size="15" />{{ t('submitOperation') }}</button>
    </template>
  </Overlay>
</template>

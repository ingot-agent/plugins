<script setup lang="ts">
import { computed } from 'vue'
import { ChartNoAxesColumn } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useRuntime } from '../stores/runtime'

const props = defineProps<{ sessionId: string }>()
const runtime = useRuntime()
const { t, n } = useI18n()
const context = computed(() => runtime.contextBySession[props.sessionId])
const format = (value?: number) => value === undefined ? t('unavailable') : n(value)
</script>
<template>
  <section class="session-usage-card" :aria-label="t('tokenUsage')">
    <header><ChartNoAxesColumn :size="16" /><h3>{{ t('tokenUsage') }}</h3><span class="usage-unit">Token</span></header>
    <dl class="usage-metrics">
      <div class="usage-session-total"><dt>{{ t('sessionTokenTotal') }}</dt><dd data-testid="session-total" :class="{ 'usage-value-small': format(runtime.totalTokenBySession[sessionId]).length > 15 }">{{ format(runtime.totalTokenBySession[sessionId]) }}</dd></div>
      <div class="usage-context-total"><dt>{{ t('currentContext') }}</dt><dd data-testid="context-total" :class="{ 'usage-value-small': format(context?.inputTokens).length > 15 }">{{ format(context?.inputTokens) }}</dd></div>
    </dl>
  </section>
</template>

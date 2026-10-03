<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useRuntime } from '../stores/runtime'

defineProps<{ sessionId: string }>()
const runtime = useRuntime()
const { t, n } = useI18n()
</script>
<template>
  <span class="session-usage" :title="t('cumulativeTokens')"><span>{{ t('cumulativeTokens') }}</span><strong>{{ runtime.totalTokenBySession[sessionId] === undefined ? t('unavailable') : n(runtime.totalTokenBySession[sessionId]!) }}</strong></span>
</template>

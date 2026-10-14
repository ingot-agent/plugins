<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Terminal, ChevronRight } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { Interaction, Part } from '../protocol'

import JsonBlock from './JsonBlock.vue'
import InteractionCard from './InteractionCard.vue'
import StatusBadge from './StatusBadge.vue'
import SessionUsage from './SessionUsage.vue'
import { useRuntime } from '../stores/runtime'
import ToolResultPart from "./ToolResultPart.vue";
const props = defineProps<{ name: string; arguments?: unknown; content?: Part[]; status?: string; error?: string; interactions?: Interaction[] }>()
const { t } = useI18n()
const runtime = useRuntime()
const open = ref(false)
const childSessions = computed(() => {
  if (!['spawn_agent', 'check_agent', 'wait_agent', 'list_agents', 'cancel_agent'].includes(props.name)) return []
  const ids = new Set<string>()
  for (const part of props.content || []) {
    if (part.kind !== 'text' || !part.text) continue
    try {
      const result = JSON.parse(part.text)
      const children = [result?.child, result?.snapshot, ...(Array.isArray(result?.page?.children) ? result.page.children : [])]
      for (const child of children) if (typeof child?.session_id === 'string' && child.session_id) ids.add(child.session_id)
    } catch { /* Plain tool output is also valid. */ }
  }
  return [...ids]
})
watch([open, childSessions], ([expanded, ids]) => {
  if (expanded) for (const id of ids) void runtime.loadSession(id)
})
</script>
<template>
  <div class="tool-card">
    <details @toggle="open = ($event.target as HTMLDetailsElement).open">
      <summary class="tool-summary"><Terminal :size="15" /><span class="font-mono truncate">{{ name }}</span><StatusBadge v-if="status" :status="status" /><ChevronRight :size="14" class="disclosure-chevron ml-auto shrink-0" /></summary>
      <div class="tool-body">
        <template v-if="arguments !== undefined"><span class="eyebrow">{{ t('arguments') }}</span><JsonBlock :value="arguments" /></template>
        <template v-if="content?.length"><span class="eyebrow">{{ t('result') }}</span><ToolResultPart :parts="content" /></template>
        <div v-for="id in childSessions" :key="id" class="child-session-usage"><code class="truncate" :title="id">{{ id }}</code><SessionUsage :session-id="id" /></div>
        <p v-if="error" class="error-text">{{ error }}</p>
      </div>
    </details>
    <InteractionCard v-for="item in interactions" :key="item.id" :interaction="item" />
  </div>
</template>

<script setup lang="ts">
import { MessageCircleQuestion, ShieldCheck } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { Interaction } from '../protocol'
import MarkdownContent from './MarkdownContent.vue'
import InteractionForm from './InteractionForm.vue'
defineProps<{ interaction: Interaction }>()
const { t } = useI18n()
</script>
<template>
  <div class="interaction-card">
    <div class="flex items-center gap-2 text-sm font-semibold accent">
      <ShieldCheck v-if="interaction.level === 'warning'" :size="17" /><MessageCircleQuestion v-else :size="17" />
      <span>{{ t('waiting') }}</span>
    </div>
    <MarkdownContent v-if="interaction.description" :text="interaction.description" />
    <InteractionForm :interaction="interaction" />
  </div>
</template>

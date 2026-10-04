<script setup lang="ts">
import SessionUsageCard from './SessionUsageCard.vue'
defineProps<{ sessionId: string }>()
</script>
<template>
  <div class="execution-panel">
    <SessionUsageCard v-if="sessionId" :session-id="sessionId" />
  </div>
</template>

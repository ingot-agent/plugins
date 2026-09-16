<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Inbox, X, ArrowUpRight, Sun, Moon, Monitor, SquareTerminal } from 'lucide-vue-next'
import type { Interaction, Operation } from './protocol'
import { useRuntime } from './stores/runtime'
import { readPreference, savePreference, applyTheme } from './theme'
import Navigation from './components/Navigation.vue'
import Overlay from './components/Overlay.vue'
import InteractionCard from './components/InteractionCard.vue'
import CommandDialog from './components/CommandDialog.vue'
const runtime = useRuntime()
const router = useRouter()
const { t, locale } = useI18n()
const collapsed = ref(readPreference('sidebar', 'open') === 'closed')
const mobileNavigation = ref(false)
const settings = ref(false)
const pending = ref(false)
const theme = ref(readPreference('theme', 'light'))
const commandDialog = reactive({ open: false, operationId: '', sessionId: '', invocationId: '', launchToken: 0 })
const dialogOperation = computed(() => runtime.operations.find(item => item.id === commandDialog.operationId))
const operationRequests = computed(() => {
  const seen = new Set<string>()
  return Object.values(runtime.interactions).filter(item => {
    const id = item.scope?.operation?.invocationId
    if (!id || seen.has(id)) return false
    seen.add(id)
    return true
  })
})
const agentRequests = computed(() => Object.values(runtime.interactions).filter(item => !item.scope?.operation))
watch(theme, value => { savePreference('theme', value); applyTheme(value) })
watch(locale, value => { savePreference('language', value); document.documentElement.lang = value === 'zh' ? 'zh-CN' : 'en' }, { immediate: true })
watch(collapsed, value => savePreference('sidebar', value ? 'closed' : 'open'))
function keyboard(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.altKey && event.key.toLowerCase() === 'n') {
    event.preventDefault()
    void router.push('/new')
  }
}
function showNavigation() {
  if (window.matchMedia('(max-width: 767px)').matches) mobileNavigation.value = true
  else collapsed.value = !collapsed.value
}
function openOperation(operation: Operation, sessionId = '') {
  commandDialog.operationId = operation.id
  commandDialog.sessionId = sessionId
  commandDialog.invocationId = ''
  commandDialog.open = true
  commandDialog.launchToken++
}
function resumeOperation(invocationId: string) {
  const invocation = runtime.operationInvocations[invocationId]
  const operation = invocation && runtime.operations.find(item => item.id === invocation.operationId)
  if (!operation) return
  commandDialog.operationId = operation.id
  commandDialog.sessionId = invocation.sessionId || ''
  commandDialog.invocationId = invocationId
  commandDialog.open = true
  commandDialog.launchToken++
  pending.value = false
}
function commandForRequest(item: Interaction) {
  const invocationId = item.scope?.operation?.invocationId
  const invocation = invocationId ? runtime.operationInvocations[invocationId] : undefined
  const operation = invocation && runtime.operations.find(value => value.id === invocation.operationId)
  return operation ? '/' + operation.group + ' ' + operation.name : t('operation')
}
function goRequest(sessionId?: string) {
  pending.value = false
  if (sessionId) void router.push('/sessions/' + encodeURIComponent(sessionId))
}
onMounted(() => { void runtime.connect(); window.addEventListener('keydown', keyboard) })
onBeforeUnmount(() => { runtime.disconnect(); window.removeEventListener('keydown', keyboard) })
</script>
<template>
  <div class="app-shell" :class="{ 'nav-collapsed': collapsed }">
    <aside class="desktop-navigation"><Navigation @navigate="mobileNavigation = false" @settings="settings = true" @collapse="collapsed = true" /></aside>
    <main class="workspace">
      <div v-if="runtime.connection === 'reconnecting'" class="connection-banner" role="status" :title="runtime.connectionError">{{ t('disconnected') }}</div>
      <RouterView v-slot="{ Component }"><component :is="Component" @navigation="showNavigation" @pending="pending = true" @operation="openOperation" /></RouterView>
    </main>
    <Overlay :open="mobileNavigation" :title="t('conversations')" drawer @update:open="mobileNavigation = $event">
      <Navigation @navigate="mobileNavigation = false" @settings="mobileNavigation = false; settings = true" @collapse="mobileNavigation = false" />
    </Overlay>
    <Overlay :open="settings" :title="t('settings')" :description="t('appearanceDescription')" @update:open="settings = $event">
      <div class="settings-section"><h3 class="field-label">{{ t('theme') }}</h3><div class="theme-options">
        <button v-for="option in [{ value: 'light', icon: Sun }, { value: 'dark', icon: Moon }, { value: 'system', icon: Monitor }]" :key="option.value" class="theme-option" :class="{ selected: theme === option.value }" :aria-pressed="theme === option.value" @click="theme = option.value"><component :is="option.icon" :size="22" /><span>{{ t(option.value) }}</span></button>
      </div></div>
      <div class="settings-section"><label class="field-label" for="language">{{ t('language') }}</label><select id="language" v-model="locale" class="field mt-3"><option value="en">English</option><option value="zh">简体中文</option></select></div>
      <div class="settings-section"><h3 class="field-label">{{ t('developer') }}</h3><RouterLink to="/operations" class="btn mt-3" @click="settings = false"><SquareTerminal :size="15" />{{ t('operationDebugger') }}</RouterLink></div>
    </Overlay>
    <Overlay :open="pending" :title="t('pending')" drawer @update:open="pending = $event">
      <div v-if="!runtime.pendingCount" class="empty-panel"><Inbox :size="28" /><p>{{ t('noPending') }}</p></div>
      <button v-for="item in operationRequests" :key="item.scope!.operation!.invocationId" type="button" class="pending-command" @click="resumeOperation(item.scope!.operation!.invocationId)">
        <div><code>{{ commandForRequest(item) }}</code><span>{{ item.description || item.name }}</span></div>
        <span class="text-button">{{ t('continueOperation') }}<ArrowUpRight :size="14" /></span>
      </button>
      <div v-for="item in agentRequests" :key="item.id" class="mb-5">
        <button v-if="item.scope?.agent?.sessionId" class="text-button mb-2" @click="goRequest(item.scope?.agent?.sessionId)">{{ t('viewConversation') }}<ArrowUpRight :size="14" /></button>
        <InteractionCard :interaction="item" />
      </div>
    </Overlay>
    <CommandDialog :open="commandDialog.open" :operation="dialogOperation" :session-id="commandDialog.sessionId" :invocation-id="commandDialog.invocationId" :launch-token="commandDialog.launchToken" @update:open="commandDialog.open = $event" @invocation="commandDialog.invocationId = $event" />
    <div class="toast-list" aria-live="polite">
      <div v-for="notice in runtime.notices.slice(-3)" :key="notice.id" class="toast" :class="{ 'error-toast': notice.level === 'error' }">
        <p>{{ notice.message }}</p><button class="icon-button" :aria-label="t('close')" @click="runtime.notices = runtime.notices.filter(item => item.id !== notice.id)"><X :size="15" /></button>
      </div>
    </div>
  </div>
</template>

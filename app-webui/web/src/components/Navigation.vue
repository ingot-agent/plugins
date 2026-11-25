<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Search, Plus, Archive, Settings2, MessageSquare, Folder, PanelLeftClose, ChevronDown, ChevronUp } from 'lucide-vue-next'
import { useRuntime } from '../stores/runtime'
import { useRelativeTime } from '../time'
import { GROUP_SESSION_LIMIT, groupSessions, isGroupCollapsed, visibleGroupSessions } from '../sessionGroups'
import type { SessionGroup, GroupView } from '../sessionGroups'
import Brand from './Brand.vue'
import SessionMenu from './SessionMenu.vue'
const emit = defineEmits<{ navigate: []; settings: []; collapse: [] }>()
const { t } = useI18n()
const router = useRouter()
const runtime = useRuntime()
const relativeTime = useRelativeTime()
const search = ref('')
const archived = ref(false)
const sessions = computed(() => runtime.orderedSessions.filter(session =>
  Boolean(session.archivedAt) === archived.value &&
  session.title.toLocaleLowerCase().includes(search.value.toLocaleLowerCase()) ||
  (session.workspace || '').toLocaleLowerCase().includes(search.value.toLocaleLowerCase())))
const hasPending = (id: string) => Object.values(runtime.interactions).some(item => item.scope?.agent?.sessionId === id)
function startInWorkspace(group: SessionGroup) {
  void router.push({ path: "/new", query: { workspace: group.key } })
  emit("navigate")
}

const groups = computed(() => groupSessions(sessions.value, t('unnamedWorkspace')))
const filtering = computed(() => search.value.trim().length > 0)
// Explicit user choices win over the defaults; a group nobody touched folds
// itself once it grows past the limit, unless the open conversation lives there.
const groupOverrides = ref<Map<string, boolean>>(new Map())
const view = computed<GroupView>(() => ({ overrides: groupOverrides.value, filtering: filtering.value, activeSessionId: runtime.activeSession || undefined }))
const collapsed = (group: SessionGroup) => isGroupCollapsed(group, view.value)
const visibleSessions = (group: SessionGroup) => visibleGroupSessions(group, view.value)
function toggleGroup(group: SessionGroup) {
  const next = new Map(groupOverrides.value)
  next.set(group.key, collapsed(group))
  groupOverrides.value = next
}
</script>
<template>
  <nav class="navigation" :aria-label="t('conversations')">
    <div class="nav-brand"><Brand wordmark /><button class="icon-button" :aria-label="t('close')" @click="$emit('collapse')"><PanelLeftClose :size="17" /></button></div>
    <div class="nav-actions">
      <RouterLink to="/new" class="btn new-conversation" @click="$emit('navigate')"><Plus :size="17" />{{ t('newChat') }}<span class="ml-auto muted text-xs">⌘ ⌥ N</span></RouterLink>
      <div class="search-field"><Search :size="15" /><input v-model="search" :placeholder="t('search')" :aria-label="t('search')" /></div>
    </div>
    <div class="nav-section"><span>{{ t(archived ? 'archived' : 'conversations') }}</span><button class="icon-button" :class="{ 'accent': archived }" :aria-label="t(archived ? 'active' : 'archived')" :aria-pressed="archived" @click="archived = !archived"><Archive :size="14" /></button></div>
    <div class="session-list">
      <template v-for="group in groups" :key="group.key">
        <div class="workspace-group-heading" :title="group.key || t('unnamedWorkspace')">
          <Folder :size="15" /><span class="truncate">{{ group.label }}</span>
          <button v-if="group.key" class="icon-button workspace-new-button" :aria-label="t('newInWorkspace')" :title="t('newInWorkspace')" @click.stop="startInWorkspace(group)"><Plus :size="14" /></button>
        </div>
        <div v-for="session in visibleSessions(group)" :key="session.id" class="session-row">
          <RouterLink :to="'/sessions/' + encodeURIComponent(session.id)" class="session-link" @click="$emit('navigate')">
            <span v-if="runtime.running(session.id).length" class="activity-dot" />
            <span v-else-if="hasPending(session.id)" class="pending-dot" />
            <MessageSquare v-else :size="15" class="session-icon" />
            <span class="truncate">{{ session.title || t('newChat') }}</span>
            <time class="session-time" :datetime="session.updatedAt" :title="session.updatedAt">{{ relativeTime(session.updatedAt) }}</time>
          </RouterLink>
          <SessionMenu :session="session" />
        </div>
        <button
          v-if="!filtering && group.sessions.length > GROUP_SESSION_LIMIT"
          type="button"
          class="group-toggle"
          :aria-expanded="!collapsed(group)"
          @click="toggleGroup(group)"
        >
          <ChevronUp v-if="!collapsed(group)" :size="14" />
          <ChevronDown v-else :size="14" />
          <span>{{ collapsed(group) ? t('showMoreSessions', { count: group.sessions.length - GROUP_SESSION_LIMIT }) : t('showFewerSessions') }}</span>
        </button>
      </template>
      <p v-if="!groups.length" class="nav-empty">{{ t(search ? 'noMatches' : 'emptySessions') }}</p>
    </div>
    <div class="nav-footer">
      <button class="nav-bottom-link w-full" @click="$emit('settings')"><Settings2 :size="17" />{{ t('settings') }}</button>
      <div class="workspace-label"><span class="workspace-avatar">i</span><div><span class="text-xs font-medium">{{ t('local') }}</span><div class="muted text-[11px]">Ingot</div></div><span class="connection-dot ml-auto" :class="{ online: runtime.connection === 'online' }" :title="t('connection.' + runtime.connection)" /></div>
    </div>
  </nav>
</template>

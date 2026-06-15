<script setup lang="ts">
import { computed } from 'vue'
import { useThreadsStore } from '@/stores/threads'
import { useGroupsStore } from '@/stores/groups'
import { useToastStore } from '@/stores/toast'
import RosterHeader from './RosterHeader.vue'
import ThreadRow from './ThreadRow.vue'
import GroupSection from './GroupSection.vue'

const threadsStore = useThreadsStore()
const groups = useGroupsStore()
const toast = useToastStore()

const emit = defineEmits<{ select: [id: string] }>()

const visibleGroups = computed(() => groups.groups.filter((g) => threadsStore.groupVisible(g.id)))
const noResults = computed(
  () =>
    threadsStore.query.trim() !== '' &&
    threadsStore.ungrouped.length === 0 &&
    visibleGroups.value.length === 0,
)

async function newThread(backend?: string): Promise<void> {
  try {
    const t = await threadsStore.create(backend)
    emit('select', t.id)
    toast.show(
      backend === 'cc' ? 'claude code session started' : 'thread minted · frame resolves at attach',
    )
  } catch {
    toast.show('could not start that thread')
  }
}
</script>

<template>
  <aside class="roster">
    <RosterHeader :count="threadsStore.threads.length" @new="newThread" />
    <div class="roster-search">
      <input
        v-model="threadsStore.query"
        class="roster-search-input"
        type="search"
        placeholder="search conversations"
        aria-label="search conversations"
        spellcheck="false"
      />
    </div>
    <div class="thread-list">
      <!-- ungrouped first: a fresh thread is always immediately visible -->
      <ThreadRow
        v-for="t in threadsStore.ungrouped"
        :key="t.id"
        :thread="t"
        :active="t.id === threadsStore.activeId"
        @select="emit('select', $event)"
      />
      <GroupSection
        v-for="g in visibleGroups"
        :key="g.id"
        :group="g"
        @select="emit('select', $event)"
      />
      <p v-if="noResults" class="roster-empty">no conversations match "{{ threadsStore.query }}"</p>
    </div>
  </aside>
</template>

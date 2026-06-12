<script setup lang="ts">
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

async function newThread(): Promise<void> {
  try {
    const t = await threadsStore.create()
    emit('select', t.id)
    toast.show('thread minted · frame resolves at attach')
  } catch {
    toast.show('could not create a thread')
  }
}
</script>

<template>
  <aside class="roster">
    <RosterHeader :count="threadsStore.threads.length" @new="newThread" />
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
        v-for="g in groups.groups"
        :key="g.id"
        :group="g"
        @select="emit('select', $event)"
      />
    </div>
  </aside>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import type { ThreadSummary } from '@/api/types'
import { displayState, isLive, stateVar, stateWord } from '@/stores/threads'
import { useGroupsStore } from '@/stores/groups'
import { useThreadsStore } from '@/stores/threads'
import { useToastStore } from '@/stores/toast'

const props = defineProps<{ thread: ThreadSummary; active: boolean }>()
const emit = defineEmits<{ select: [id: string] }>()

const groups = useGroupsStore()
const threadsStore = useThreadsStore()
const toast = useToastStore()

const ds = computed(() => displayState(props.thread))
const word = computed(() => stateWord(ds.value))
const cvar = computed(() => stateVar(ds.value))
const live = computed(() => isLive(ds.value))

const menuOpen = ref(false)

function relTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  const mins = Math.round(diff / 60000)
  if (mins < 1) return 'now'
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.round(hrs / 24)}d ago`
}

async function moveTo(groupId: string | null): Promise<void> {
  menuOpen.value = false
  try {
    await groups.assign(props.thread.id, groupId)
    await threadsStore.poll()
    toast.show(
      groupId
        ? 'moved to group · roster updates on the next poll'
        : 'removed from group · back to ungrouped',
    )
  } catch {
    toast.show('could not move the thread')
  }
}
</script>

<template>
  <div
    class="thread"
    :class="{ active }"
    :style="{ '--state-c': cvar }"
    @click="emit('select', thread.id)"
  >
    <div class="t-title">{{ thread.title }}</div>
    <div class="t-state">{{ word }}</div>
    <div class="t-preview">{{ thread.preview }}</div>
    <div class="t-meta">
      <span class="t-dot" :class="{ live }"></span>
      <span class="t-frame">{{ thread.frame }}</span>
      <span>{{ thread.user_msgs }} msgs · {{ relTime(thread.updated_at) }}</span>
      <button class="t-move" title="move to group" @click.stop="menuOpen = !menuOpen">⋯</button>
    </div>
    <div v-if="menuOpen" class="move-menu" @click.stop>
      <div class="mm-label">move to</div>
      <button @click="moveTo(null)">ungrouped</button>
      <button v-for="g in groups.groups" :key="g.id" @click="moveTo(g.id)">
        {{ g.name }}
      </button>
    </div>
  </div>
</template>

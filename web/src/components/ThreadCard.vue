<script setup lang="ts">
// carlos web · conversation card (WB-2 home board).
// The board variant of ThreadRow: the same fields (avatar, title, preview,
// state dot, frame, rel-time) laid out as a card per web/mockups/home-by-repo.
// No left-stripe category coding; backend identity rides the mascot avatar.
import { computed } from 'vue'
import type { ThreadSummary } from '@/api/types'
import { displayState, isLive, stateVar, stateWord } from '@/stores/threads'
import BackendMark from './BackendMark.vue'

const props = defineProps<{ thread: ThreadSummary }>()
const emit = defineEmits<{ select: [id: string] }>()

const ds = computed(() => displayState(props.thread))
const word = computed(() => stateWord(ds.value))
const cvar = computed(() => stateVar(ds.value))
const live = computed(() => isLive(ds.value))

function relTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  const mins = Math.round(diff / 60000)
  if (mins < 1) return 'now'
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.round(hrs / 24)}d ago`
}
</script>

<template>
  <article class="thread-card" :style="{ '--state-c': cvar }" @click="emit('select', thread.id)">
    <span class="tc-avatar" :data-backend="thread.backend">
      <BackendMark :backend="thread.backend" :size="22" />
    </span>
    <div class="tc-body">
      <div class="tc-title">{{ thread.title }}</div>
      <div class="tc-preview">{{ thread.preview }}</div>
      <div class="tc-meta">
        <span class="tc-dot" :class="{ live }"></span>
        <span v-if="thread.frame" class="tc-frame">{{ thread.frame }}</span>
        <span>{{ thread.user_msgs }} msgs · {{ relTime(thread.updated_at) }}</span>
        <span class="tc-state">{{ word }}</span>
      </div>
    </div>
  </article>
</template>

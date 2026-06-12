<script setup lang="ts">
// Per-thread header. Owns the persistent at-a-glance row (title, state, frame,
// attach control) plus the expandable details panel that absorbed the old
// "this thread" rail card. The rail itself now belongs to sub-agents only.

import { computed, ref, watch } from 'vue'
import type { ThreadSummary } from '@/api/types'
import { displayState, stateVar, stateWord } from '@/stores/threads'
import ThreadMetaPanel from './ThreadMetaPanel.vue'

const props = defineProps<{ thread: ThreadSummary }>()
const emit = defineEmits<{
  attach: []
  detach: []
  attachForeign: []
  delete: [id: string]
}>()

const ds = computed(() => displayState(props.thread))
const isForeign = computed(() => ds.value === 'foreign')

// The frame resolves at attach; a detached summary carries "". Render a
// dash for that rather than an empty chip.
const frameLabel = computed(() => props.thread.frame || '-')

// progressive disclosure: the long-tail meta hides behind "details".
const open = ref(false)
// switching threads closes the panel; details are a per-thread inspection.
watch(
  () => props.thread.id,
  () => {
    open.value = false
  },
)
</script>

<template>
  <div class="stage-head" :style="{ '--state-c': stateVar(ds) }">
    <div class="sh-row">
      <span class="s-title" :title="thread.title">{{ thread.title }}</span>
      <span class="s-state">{{ stateWord(ds) }}</span>
      <span class="s-frame" title="frame">{{ frameLabel }}</span>
      <span class="spacer"></span>
      <button v-if="isForeign" class="sh-act" @click="emit('attachForeign')">attach</button>
      <button v-else-if="thread.attached" class="sh-act" @click="emit('detach')">detach</button>
      <button v-else class="sh-act primary" @click="emit('attach')">attach</button>
      <button
        class="sh-more"
        :class="{ open }"
        :aria-expanded="open"
        aria-label="thread details"
        @click="open = !open"
      >
        details <span class="chev">▾</span>
      </button>
    </div>
    <ThreadMetaPanel v-if="open" :thread="thread" @delete="emit('delete', $event)" />
  </div>
</template>

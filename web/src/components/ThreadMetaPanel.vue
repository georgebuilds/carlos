<script setup lang="ts">
// The disclosed half of the stage header: the long-tail thread meta that the
// old rail card used to carry (id, model, backend, attached, messages) plus
// the two-step delete. Only mounted while the header's "details" is open.

import { computed, ref, watch } from 'vue'
import type { ThreadSummary } from '@/api/types'
import { displayState } from '@/stores/threads'

const props = defineProps<{ thread: ThreadSummary }>()
const emit = defineEmits<{ delete: [id: string] }>()

const isForeign = computed(() => displayState(props.thread) === 'foreign')

const attachedLabel = computed(() => {
  if (props.thread.attached) return 'yes, this process'
  if (isForeign.value) return 'no · owned by the TUI'
  return 'no'
})

// two-step confirm: the first click arms, the second commits the hard delete.
const confirming = ref(false)
// reset the armed state if the panel switches to a different thread.
watch(
  () => props.thread.id,
  () => {
    confirming.value = false
  },
)
function onConfirm(): void {
  confirming.value = false
  emit('delete', props.thread.id)
}
</script>

<template>
  <div class="meta-panel">
    <div class="mp-pairs">
      <span class="mp-pair"
        ><span class="mp-k">id</span
        ><span class="mp-v mono" :title="thread.id">{{ thread.id }}</span></span
      >
      <span class="mp-pair"
        ><span class="mp-k">model</span
        ><span class="mp-v mono" :title="thread.model">{{ thread.model }}</span></span
      >
      <span class="mp-pair"
        ><span class="mp-k">backend</span><span class="mp-v mono">{{ thread.backend }}</span></span
      >
      <span class="mp-pair"
        ><span class="mp-k">attached</span><span class="mp-v">{{ attachedLabel }}</span></span
      >
      <span class="mp-pair"
        ><span class="mp-k">messages</span><span class="mp-v">{{ thread.user_msgs }}</span></span
      >
      <span class="spacer"></span>
      <button v-if="!confirming" class="btn-delete" @click="confirming = true">delete</button>
    </div>
    <div v-if="confirming" class="delete-confirm">
      <p class="dc-copy">Permanently delete this conversation? This cannot be undone.</p>
      <div class="dc-actions">
        <button class="btn-ghost" @click="confirming = false">cancel</button>
        <button class="btn-ghost danger" @click="onConfirm">yes, delete</button>
      </div>
    </div>
  </div>
</template>

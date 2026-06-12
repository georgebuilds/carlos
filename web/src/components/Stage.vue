<script setup lang="ts">
import { computed } from 'vue'
import type { ThreadSummary, ApprovalDecision, WireEvent } from '@/api/types'
import { displayState } from '@/stores/threads'
import type { PendingApproval } from '@/stores/approvals'
import StageHeader from './StageHeader.vue'
import GuardBanner from './GuardBanner.vue'
import TranscriptFeed from './TranscriptFeed.vue'
import ApprovalBanner from './ApprovalBanner.vue'
import Composer from './Composer.vue'

const props = defineProps<{
  thread: ThreadSummary
  events: WireEvent[]
  delta: string
  approvals: PendingApproval[]
}>()

const emit = defineEmits<{
  send: [text: string]
  resolve: [requestId: string, decision: ApprovalDecision]
  attach: []
  detach: []
  attachForeign: []
  delete: [id: string]
}>()

const isForeign = computed(() => displayState(props.thread) === 'foreign')
const canSend = computed(() => props.thread.attached && !isForeign.value)

const placeholder = computed(() => {
  if (isForeign.value) return 'read-only: this thread belongs to the TUI right now'
  if (!props.thread.attached) return 'attach to this thread to talk to it'
  return 'tell carlos what is next...'
})

// the first pending approval drives the banner; the banner carries the queue
// count since the rail no longer guarantees a home for the overflow.
const topApproval = computed(() => props.approvals[0] ?? null)
const queued = computed(() => Math.max(0, props.approvals.length - 1))
</script>

<template>
  <main class="stage">
    <StageHeader
      :thread="thread"
      @attach="emit('attach')"
      @detach="emit('detach')"
      @attach-foreign="emit('attachForeign')"
      @delete="emit('delete', $event)"
    />
    <GuardBanner v-if="isForeign" :heartbeat-age="thread.heartbeat_age" />
    <TranscriptFeed :events="events" :delta="delta" />
    <ApprovalBanner
      v-if="topApproval"
      :approval="topApproval"
      :queued="queued"
      @resolve="(rid, d) => emit('resolve', rid, d)"
    />
    <Composer
      :disabled="!canSend"
      :placeholder="placeholder"
      @send="(t) => emit('send', t)"
    />
  </main>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, watch } from 'vue'
import { api, ApiError } from '@/api/client'
import type { ApprovalDecision, ChildrenData, WireEvent } from '@/api/types'
import { displayState, useThreadsStore } from '@/stores/threads'
import { useGroupsStore } from '@/stores/groups'
import { useConnectionStore } from '@/stores/connection'
import { useTranscriptStore } from '@/stores/transcript'
import { useApprovalsStore } from '@/stores/approvals'
import { useToastStore } from '@/stores/toast'
import TopBar from './components/TopBar.vue'
import Roster from './components/Roster.vue'
import Stage from './components/Stage.vue'
import EmptyStage from './components/EmptyStage.vue'
import Rail from './components/Rail.vue'
import Toast from './components/Toast.vue'

const conn = useConnectionStore()
const threadsStore = useThreadsStore()
const groups = useGroupsStore()
const transcript = useTranscriptStore()
const approvals = useApprovalsStore()
const toast = useToastStore()

const active = computed(() => threadsStore.active)
const activeId = computed(() => active.value?.id ?? null)

const activeEvents = computed<WireEvent[]>(() =>
  activeId.value ? transcript.events(activeId.value) : [],
)
const activeDelta = computed(() => (activeId.value ? transcript.delta(activeId.value) : ''))
const activeApprovals = computed(() =>
  activeId.value ? approvals.pendingFor(activeId.value) : [],
)
const activeChildren = computed(() =>
  activeId.value ? threadsStore.children[activeId.value] ?? [] : [],
)

// ── boot ────────────────────────────────────────────────────────────────
onMounted(async () => {
  conn.boot()
  await conn.loadMeta()
  await Promise.all([threadsStore.poll().catch(() => {}), groups.refresh().catch(() => {})])
  threadsStore.startPolling()
})

onBeforeUnmount(() => {
  threadsStore.stopPolling()
  if (activeId.value) transcript.stopStream(activeId.value)
})

// ── per-thread wiring: when the active thread changes, (re)load + stream ──
let lastStreamed: string | null = null

watch(
  activeId,
  async (id, prev) => {
    if (prev && prev !== id) transcript.stopStream(prev)
    if (!id) return
    const t = threadsStore.threads.find((x) => x.id === id)
    if (!t) return

    // backfill persisted events for every thread (read-only or not)
    try {
      await transcript.backfill(id)
    } catch {
      toast.show('could not load the transcript')
    }
    void threadsStore.loadChildren(id).catch(() => {})

    // only open an SSE stream for attached, non-foreign threads.
    const foreign = displayState(t) === 'foreign'
    if (t.attached && !foreign) {
      lastStreamed = id
      transcript.startStream(id, conn.token, (ev) => onStreamEvent(id, ev))
    }
  },
  { immediate: true },
)

// approvals + ownership ride the SSE event stream.
function onStreamEvent(id: string, ev: WireEvent): void {
  if (approvals.ingest(id, ev)) {
    if (ev.kind === 'approval_resolved') {
      toast.show('approval_resolved · fanned out to every connected tab')
    }
  }
  // a children snapshot mid-stream is the moment the crew column appears:
  // adopt it so the rail slides in when the first sub-agent spawns.
  if (ev.kind === 'children') {
    const data = ev.data as unknown as ChildrenData
    if (Array.isArray(data.children)) threadsStore.setChildren(id, data.children)
  }
}

// ── actions ──────────────────────────────────────────────────────────────
async function onSend(text: string): Promise<void> {
  const id = activeId.value
  if (!id) return
  try {
    await api.sendMessage(id, text)
    await threadsStore.poll().catch(() => {})
  } catch (e) {
    toast.show(e instanceof ApiError ? e.message : 'send failed')
  }
}

async function onResolve(requestId: string, decision: ApprovalDecision): Promise<void> {
  const id = activeId.value
  if (!id) return
  try {
    await approvals.resolve(id, requestId, decision, (msg) => toast.show(msg))
    toast.show(
      decision === 'deny'
        ? 'denied · approval_resolved fanned out'
        : decision === 'allow_always'
          ? 'allowed · added to this thread always-allow cache'
          : 'allowed once · request resolved',
    )
  } catch (e) {
    toast.show(e instanceof ApiError ? e.message : 'could not resolve')
  }
}

async function onAttach(): Promise<void> {
  const id = activeId.value
  if (!id) return
  try {
    await threadsStore.attach(id)
    toast.show('attached · chatglue loop and heartbeat started')
    // open the stream now that we own it
    transcript.startStream(id, conn.token, (ev) => onStreamEvent(id, ev))
    lastStreamed = id
  } catch (e) {
    toast.show(e instanceof ApiError ? e.message : 'attach failed')
  }
}

async function onDetach(): Promise<void> {
  const id = activeId.value
  if (!id) return
  try {
    await threadsStore.detach(id)
    transcript.stopStream(id)
    if (lastStreamed === id) lastStreamed = null
    toast.show('detached · loop stopped, transcript stays readable')
  } catch (e) {
    toast.show(e instanceof ApiError ? e.message : 'detach failed')
  }
}

async function onAttachForeign(): Promise<void> {
  const id = activeId.value
  if (!id) return
  try {
    await threadsStore.attach(id)
    toast.show('attached')
  } catch (e) {
    if (e instanceof ApiError && e.isOwned) {
      toast.show('409 thread_owned · the heartbeat is fresh. Close the TUI session or pick another thread.')
      return
    }
    toast.show(e instanceof ApiError ? e.message : 'attach refused')
  }
}

function onSelect(id: string): void {
  threadsStore.setActive(id)
}

async function onDelete(id: string): Promise<void> {
  // stop any live stream for this thread before the row disappears.
  transcript.stopStream(id)
  if (lastStreamed === id) lastStreamed = null
  try {
    const { deleted } = await threadsStore.remove(id)
    toast.show(
      deleted > 1 ? `deleted conversation and ${deleted - 1} sub-agents` : 'deleted conversation',
    )
  } catch (e) {
    if (e instanceof ApiError && e.isThreadLive) {
      toast.show('this thread is live; detach it first, then delete')
      return
    }
    toast.show(e instanceof ApiError ? e.message : 'delete failed')
  }
}

// keyboard: d/a resolve the top pending approval for the active thread.
function onKeydown(e: KeyboardEvent): void {
  const target = e.target as HTMLElement | null
  if (target && (target.tagName === 'TEXTAREA' || target.tagName === 'INPUT')) return
  const top = activeApprovals.value[0]
  if (!top) return
  if (e.key === 'd') void onResolve(top.requestId, 'deny')
  if (e.key === 'a') void onResolve(top.requestId, 'allow')
}

onMounted(() => window.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div class="app">
    <TopBar />
    <div class="panes">
      <Roster @select="onSelect" />
      <Stage
        v-if="active"
        :thread="active"
        :events="activeEvents"
        :delta="activeDelta"
        :approvals="activeApprovals"
        @send="onSend"
        @resolve="onResolve"
        @attach="onAttach"
        @detach="onDetach"
        @attach-foreign="onAttachForeign"
        @delete="onDelete"
      />
      <EmptyStage v-else />
      <Rail :approvals="activeApprovals" :children="activeChildren" />
    </div>
    <Toast />
  </div>
</template>

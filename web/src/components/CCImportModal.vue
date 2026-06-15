<script setup lang="ts">
// carlos web · open-existing-CC-session modal (WA-2).
// GETs /api/cc/importable, lists historical Claude Code sessions (title, repo,
// age, backend mascot); picking one POSTs /api/cc/import, refreshes the
// roster, toasts, and closes. Empty + error states are handled inline.
import { onMounted, ref } from 'vue'
import type { ThreadSummary } from '@/api/types'
import { useThreadsStore } from '@/stores/threads'
import { useToastStore } from '@/stores/toast'
import BackendMark from './BackendMark.vue'

const emit = defineEmits<{ close: []; imported: [id: string] }>()

const threads = useThreadsStore()
const toast = useToastStore()

const loading = ref(true)
const error = ref(false)
const sessions = ref<ThreadSummary[]>([])
const importingId = ref<string | null>(null)

async function load(): Promise<void> {
  loading.value = true
  error.value = false
  try {
    sessions.value = await threads.importable()
  } catch {
    error.value = true
  } finally {
    loading.value = false
  }
}

function relTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  const mins = Math.round(diff / 60000)
  if (mins < 1) return 'now'
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.round(hrs / 24)}d ago`
}

async function pick(s: ThreadSummary): Promise<void> {
  if (importingId.value) return
  importingId.value = s.id
  try {
    const t = await threads.importSession(s.id)
    toast.show('claude code session imported · now in your roster')
    emit('imported', t.id)
    emit('close')
  } catch {
    toast.show('could not import that session')
    importingId.value = null
  }
}

onMounted(load)
</script>

<template>
  <div class="cc-modal-backdrop" @click="emit('close')"></div>
  <div class="cc-modal" role="dialog" aria-label="open existing Claude Code session" @click.stop>
    <div class="cc-modal-head">
      <span class="cc-modal-title">open existing Claude Code session</span>
      <button class="cc-modal-x" aria-label="close" @click="emit('close')">×</button>
    </div>

    <p v-if="loading" class="cc-modal-note">looking for Claude Code sessions…</p>

    <template v-else-if="error">
      <p class="cc-modal-note">could not load Claude Code sessions.</p>
      <button class="cc-modal-retry" @click="load">try again</button>
    </template>

    <p v-else-if="sessions.length === 0" class="cc-modal-note">
      no other Claude Code sessions found.
    </p>

    <ul v-else class="cc-list">
      <li v-for="s in sessions" :key="s.id">
        <button class="cc-row" :disabled="importingId !== null" @click="pick(s)">
          <span class="cc-row-avatar" :data-backend="s.backend">
            <BackendMark :backend="s.backend" :size="20" />
          </span>
          <span class="cc-row-body">
            <span class="cc-row-title">{{ s.title }}</span>
            <span class="cc-row-meta">
              <span v-if="s.repo" class="cc-row-repo">{{ s.repo.name }}</span>
              <span>{{ relTime(s.updated_at) }}</span>
            </span>
          </span>
          <span v-if="importingId === s.id" class="cc-row-state">importing…</span>
        </button>
      </li>
    </ul>
  </div>
</template>

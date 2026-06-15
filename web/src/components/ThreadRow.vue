<script setup lang="ts">
import { computed, ref } from 'vue'
import type { ThreadSummary } from '@/api/types'
import { displayState, isLive, stateVar, stateWord } from '@/stores/threads'
import BackendMark from './BackendMark.vue'
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
const isCarlos = computed(() => props.thread.backend === 'carlos')
const isHidden = computed(() => !!props.thread.hidden)

const menuOpen = ref(false)
const newGroupMode = ref(false)
const newGroupName = ref('')
const confirmRemove = ref(false)

function closeMenu(): void {
  menuOpen.value = false
  newGroupMode.value = false
  newGroupName.value = ''
  confirmRemove.value = false
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

async function moveTo(groupId: string | null): Promise<void> {
  closeMenu()
  try {
    await groups.assign(props.thread.id, groupId)
    await threadsStore.poll()
    toast.show(groupId ? 'moved to group' : 'removed from group · back to ungrouped')
  } catch {
    toast.show('could not move the thread')
  }
}

async function createGroupAndMove(): Promise<void> {
  const name = newGroupName.value.trim()
  if (!name) return
  closeMenu()
  try {
    const g = await groups.create(name)
    await groups.assign(props.thread.id, g.id)
    await threadsStore.poll()
    toast.show(`group "${g.name}" created · thread moved`)
  } catch {
    toast.show('could not create the group')
  }
}

async function doHide(): Promise<void> {
  closeMenu()
  try {
    await threadsStore.hide(props.thread.id)
    toast.show('hidden from this list · "show hidden" to restore')
  } catch {
    toast.show('could not hide the thread')
  }
}

async function doUnhide(): Promise<void> {
  closeMenu()
  try {
    await threadsStore.unhide(props.thread.id)
    toast.show('restored to the list')
  } catch {
    toast.show('could not restore the thread')
  }
}

async function doRemove(): Promise<void> {
  closeMenu()
  try {
    await threadsStore.remove(props.thread.id)
    toast.show(isCarlos.value ? 'conversation deleted' : 'claude code session deleted')
  } catch (e) {
    const msg = e instanceof Error && /live/.test(e.message) ? 'thread is live · detach first' : 'could not delete'
    toast.show(msg)
  }
}
</script>

<template>
  <div
    class="thread"
    :class="{ active, hidden: isHidden }"
    :style="{ '--state-c': cvar }"
    @click="emit('select', thread.id)"
  >
    <div class="t-avatar" :data-backend="thread.backend">
      <BackendMark :backend="thread.backend" :size="22" />
    </div>
    <div class="t-body">
      <div class="t-title-text">{{ thread.title }}</div>
      <div class="t-preview">{{ thread.preview }}</div>
      <div class="t-meta">
        <span class="t-dot" :class="{ live }"></span>
        <span v-if="thread.frame" class="t-frame">{{ thread.frame }}</span>
        <span>{{ thread.user_msgs }} msgs · {{ relTime(thread.updated_at) }}</span>
        <button class="t-move" title="thread actions" @click.stop="menuOpen = !menuOpen">⋯</button>
      </div>
    </div>
    <div class="t-state">{{ word }}</div>

    <template v-if="menuOpen">
      <div class="menu-backdrop" @click.stop="closeMenu"></div>
      <div class="move-menu" @click.stop>
        <div class="mm-label">move to</div>
        <button @click="moveTo(null)">ungrouped</button>
        <button v-for="g in groups.groups" :key="g.id" @click="moveTo(g.id)">{{ g.name }}</button>
        <button v-if="!newGroupMode" class="mm-add" @click="newGroupMode = true">+ new group…</button>
        <input
          v-else
          v-model="newGroupName"
          class="mm-input"
          placeholder="group name, enter to create"
          @keyup.enter="createGroupAndMove"
          @keyup.escape="newGroupMode = false"
        />

        <div class="mm-sep"></div>
        <button v-if="isHidden" @click="doUnhide">restore to list</button>
        <button v-else-if="!isCarlos" @click="doHide">hide from list</button>
        <button v-if="!confirmRemove" class="mm-danger" @click="confirmRemove = true">
          {{ isCarlos ? 'delete conversation' : 'delete session…' }}
        </button>
        <div v-else class="mm-confirm">
          <p>{{ isCarlos ? 'delete this conversation? cannot be undone.' : 'delete the Claude Code session file? removes it from Claude Code too.' }}</p>
          <div class="mm-confirm-actions">
            <button @click="confirmRemove = false">cancel</button>
            <button class="mm-danger" @click="doRemove">yes, delete</button>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

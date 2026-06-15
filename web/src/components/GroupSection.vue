<script setup lang="ts">
import { computed, ref } from 'vue'
import type { Group, ThreadSummary } from '@/api/types'
import { useGroupsStore } from '@/stores/groups'
import { useThreadsStore } from '@/stores/threads'
import { useToastStore } from '@/stores/toast'
import ThreadRow from './ThreadRow.vue'

const props = defineProps<{ group: Group }>()
const emit = defineEmits<{ select: [id: string] }>()

const groups = useGroupsStore()
const threadsStore = useThreadsStore()
const toast = useToastStore()

const collapsed = computed(() => groups.isCollapsed(props.group.id))
const members = computed<ThreadSummary[]>(() => threadsStore.membersOf(props.group.id))
const roll = computed(() => threadsStore.rollup(props.group.id))

const menuOpen = ref(false)
const renameMode = ref(false)
const renameName = ref('')
const confirmDelete = ref(false)

function openMenu(): void {
  menuOpen.value = true
  renameName.value = props.group.name
}
function closeMenu(): void {
  menuOpen.value = false
  renameMode.value = false
  confirmDelete.value = false
}
async function doRename(): Promise<void> {
  const n = renameName.value.trim()
  if (!n) return
  closeMenu()
  try {
    await groups.rename(props.group.id, n)
    toast.show('group renamed')
  } catch {
    toast.show('could not rename the group')
  }
}
async function doDelete(): Promise<void> {
  closeMenu()
  try {
    await groups.remove(props.group.id)
    await threadsStore.poll()
    toast.show('group deleted · threads back to ungrouped')
  } catch {
    toast.show('could not delete the group')
  }
}
</script>

<template>
  <div class="group-head" :class="{ collapsed }" @click="groups.toggle(group.id)">
    <span class="chev">&#9660;</span>
    <span class="g-name">{{ group.name }}</span>
    <span class="g-count">{{ members.length }}</span>
    <span class="g-badges" v-if="collapsed">
      <span v-if="roll.running" class="badge b-running">{{ roll.running }} running</span>
      <span v-if="roll.blocked" class="badge b-blocked">{{ roll.blocked }} needs a call</span>
      <span v-if="roll.turn" class="badge b-turn">{{ roll.turn }} your turn</span>
    </span>
    <button class="g-menu-btn" title="group actions" @click.stop="openMenu">⋯</button>

    <template v-if="menuOpen">
      <div class="menu-backdrop" @click.stop="closeMenu"></div>
      <div class="move-menu g-menu" @click.stop>
        <input
          v-if="renameMode"
          v-model="renameName"
          class="mm-input"
          @keyup.enter="doRename"
          @keyup.escape="renameMode = false"
        />
        <button v-else @click="renameMode = true">rename group</button>
        <div class="mm-sep"></div>
        <button v-if="!confirmDelete" class="mm-danger" @click="confirmDelete = true">delete group</button>
        <div v-else class="mm-confirm">
          <p>delete this group? its threads revert to ungrouped.</p>
          <div class="mm-confirm-actions">
            <button @click="confirmDelete = false">cancel</button>
            <button class="mm-danger" @click="doDelete">yes, delete</button>
          </div>
        </div>
      </div>
    </template>
  </div>
  <template v-if="!collapsed">
    <ThreadRow
      v-for="t in members"
      :key="t.id"
      :thread="t"
      :active="t.id === threadsStore.activeId"
      @select="emit('select', $event)"
    />
  </template>
</template>

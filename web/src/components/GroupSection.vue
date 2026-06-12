<script setup lang="ts">
import { computed } from 'vue'
import type { Group, ThreadSummary } from '@/api/types'
import { useGroupsStore } from '@/stores/groups'
import { useThreadsStore } from '@/stores/threads'
import ThreadRow from './ThreadRow.vue'

const props = defineProps<{ group: Group }>()
const emit = defineEmits<{ select: [id: string] }>()

const groups = useGroupsStore()
const threadsStore = useThreadsStore()

const collapsed = computed(() => groups.isCollapsed(props.group.id))
const members = computed<ThreadSummary[]>(() => threadsStore.membersOf(props.group.id))
const roll = computed(() => threadsStore.rollup(props.group.id))
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

<script setup lang="ts">
// The right column is the crew column now: it exists for sub-agents, and it
// only takes space when the active thread has at least one (any ever spawned,
// not just live ones, so a finished thread's crew stays inspectable). The
// aside stays in the DOM and collapses its width so the appearance of the
// first sub-agent reads as a column sliding in, not a layout jump. The global
// prefers-reduced-motion rule neutralizes the transition.

import { computed } from 'vue'
import type { ChildSnapshot } from '@/api/types'
import type { PendingApproval } from '@/stores/approvals'
import ApprovalCard from './ApprovalCard.vue'
import CrewList from './CrewList.vue'

const props = defineProps<{
  approvals: PendingApproval[]
  children: ChildSnapshot[]
}>()

const visible = computed(() => props.children.length > 0)
</script>

<template>
  <aside class="rail" :class="{ collapsed: !visible }" :aria-hidden="!visible">
    <div v-if="visible" class="rail-inner">
      <ApprovalCard v-for="a in approvals" :key="a.requestId" :approval="a" />
      <CrewList :children="children" />
    </div>
  </aside>
</template>

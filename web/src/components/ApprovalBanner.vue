<script setup lang="ts">
import { computed } from 'vue'
import type { PendingApproval } from '@/stores/approvals'
import { stringifyInput } from '@/api/render'
import type { ApprovalDecision } from '@/api/types'

// queued: how many more approvals wait behind this one. The banner is the
// queue's one guaranteed surface now that the rail collapses without sub-agents.
const props = defineProps<{ approval: PendingApproval; queued?: number }>()
const emit = defineEmits<{ resolve: [requestId: string, decision: ApprovalDecision] }>()

function resolve(decision: ApprovalDecision): void {
  emit('resolve', props.approval.requestId, decision)
}

const inputStr = computed(() => stringifyInput(props.approval.input))
</script>

<template>
  <div class="approval">
    <div class="a-head">
      <span class="a-tag">needs your call</span>
      <span class="a-reason">
        <template v-if="approval.layerReason">{{ approval.layerReason }} · </template
        >{{ approval.requestId }}
      </span>
      <span v-if="queued" class="a-queue">+{{ queued }} more waiting</span>
    </div>
    <div class="a-cmd"><span class="tool">{{ approval.name }}</span> · {{ inputStr }}</div>
    <div class="a-btns">
      <button class="deny" @click="resolve('deny')">deny</button>
      <button class="allow" @click="resolve('allow')">allow once</button>
      <button class="always" @click="resolve('allow_always')">
        always allow {{ approval.name }}
      </button>
      <span class="a-kbd"><kbd>d</kbd> deny · <kbd>a</kbd> allow</span>
    </div>
  </div>
</template>

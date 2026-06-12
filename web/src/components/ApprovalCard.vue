<script setup lang="ts">
import type { PendingApproval } from '@/stores/approvals'

defineProps<{ approval: PendingApproval }>()
</script>

<template>
  <div class="rail-card waiting">
    <span class="rc-tag">waiting on you</span>
    <h3>pending approval</h3>
    <dl class="kv">
      <dt>tool</dt><dd class="mono">{{ approval.name }}</dd>
      <dt>request</dt><dd class="mono">{{ approval.requestId }}</dd>
    </dl>
    <div class="rail-note" style="margin-top: 8px; padding: 0">
      The loop is blocked in-goroutine until you answer (spec §10). No timeout:
      carlos waits as long as you do.
    </div>
  </div>
</template>

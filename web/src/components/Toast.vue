<script setup lang="ts">
import { useToastStore } from '@/stores/toast'
const toast = useToastStore()
</script>

<template>
  <!-- html is composed from trusted strings only (wire-effect narration) -->
  <div class="toast" :class="{ show: toast.visible }" v-html="toast.html"></div>
</template>

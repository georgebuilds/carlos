<script setup lang="ts">
// carlos web · one repo section on the by-repo home board (WB-2).
// Header = repo name + dimmed root path + a thin rule + member count;
// catch-all ("No repository", root '') dims the name and drops the path.
// Cards are a responsive grid of ThreadCard. Matches web/mockups/home-by-repo.
import type { RepoGroup } from '@/stores/threads'
import ThreadCard from './ThreadCard.vue'

defineProps<{ group: RepoGroup }>()
const emit = defineEmits<{ select: [id: string] }>()
</script>

<template>
  <section class="repo-sec" :class="{ nogit: group.root === '' }">
    <div class="repo-head">
      <span class="repo-name">{{ group.name }}</span>
      <span v-if="group.root" class="repo-path">{{ group.root }}</span>
      <span class="repo-rule"></span>
      <span class="repo-count">{{ group.threads.length }}</span>
    </div>
    <div class="repo-grid">
      <ThreadCard
        v-for="t in group.threads"
        :key="t.id"
        :thread="t"
        @select="emit('select', $event)"
      />
    </div>
  </section>
</template>

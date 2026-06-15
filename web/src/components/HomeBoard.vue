<script setup lang="ts">
// carlos web · launch board (WB-2), shown when no thread is selected.
// A repo-grouped board with a [ By repo | By group ] segmented toggle in its
// header (choice persisted via the homeview store, default "by repo"). The
// "By group" mode reuses the existing manual-group rendering. Selecting a card
// (or group row) emits select, exactly like the roster.
import { computed } from 'vue'
import { useThreadsStore } from '@/stores/threads'
import { useGroupsStore } from '@/stores/groups'
import { useHomeViewStore } from '@/stores/homeview'
import RepoSection from './RepoSection.vue'
import GroupSection from './GroupSection.vue'
import ThreadRow from './ThreadRow.vue'

const threads = useThreadsStore()
const groups = useGroupsStore()
const home = useHomeViewStore()

const emit = defineEmits<{ select: [id: string] }>()

const sections = computed(() => threads.byRepo)
const visibleGroups = computed(() =>
  groups.groups.filter((g) => threads.groupVisible(g.id)),
)
const empty = computed(() => threads.threads.filter((t) => !t.parent_id).length === 0)
</script>

<template>
  <main class="home">
    <header class="home-head">
      <div>
        <h1 class="home-title">home</h1>
        <p class="home-sub">grouped by git repository · newest activity first</p>
      </div>
      <span class="home-spacer"></span>
      <div class="home-toggle" role="tablist" aria-label="home view">
        <button
          class="ht-btn"
          :class="{ on: home.view === 'repo' }"
          role="tab"
          :aria-selected="home.view === 'repo'"
          @click="home.setView('repo')"
        >
          By repo
        </button>
        <button
          class="ht-btn"
          :class="{ on: home.view === 'group' }"
          role="tab"
          :aria-selected="home.view === 'group'"
          @click="home.setView('group')"
        >
          By group
        </button>
      </div>
    </header>

    <div class="home-body">
      <div v-if="empty" class="home-empty">
        <div class="big">no thread yet.</div>
        <p>Mint a fresh one with + new, or open an existing Claude Code session.</p>
      </div>

      <template v-else-if="home.view === 'repo'">
        <RepoSection
          v-for="g in sections"
          :key="g.root || '__none__'"
          :group="g"
          @select="emit('select', $event)"
        />
      </template>

      <div v-else class="home-groups">
        <ThreadRow
          v-for="t in threads.ungrouped"
          :key="t.id"
          :thread="t"
          :active="t.id === threads.activeId"
          @select="emit('select', $event)"
        />
        <GroupSection
          v-for="g in visibleGroups"
          :key="g.id"
          :group="g"
          @select="emit('select', $event)"
        />
      </div>
    </div>
  </main>
</template>

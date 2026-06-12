<script setup lang="ts">
import { computed } from 'vue'
import { useConnectionStore } from '@/stores/connection'
import { useThemeStore, type ThemeMode } from '@/stores/theme'

const conn = useConnectionStore()
const addr = computed(() => conn.meta?.addr ?? '127.0.0.1:7777')
const version = computed(() => conn.meta?.version ?? 'dev')

const theme = useThemeStore()
const GLYPH: Record<ThemeMode, string> = { system: '◐', light: '☀︎', dark: '☾' }
const NEXT: Record<ThemeMode, ThemeMode> = { system: 'light', light: 'dark', dark: 'system' }
const themeGlyph = computed(() => GLYPH[theme.mode])
const themeLabel = computed(() => `theme: ${theme.mode} · click for ${NEXT[theme.mode]}`)
</script>

<template>
  <div class="topbar">
    <a class="mark" href="#" @click.prevent>
      <span class="cap">🧢</span>
      <span class="name">carlos</span>
      <span class="sub">web</span>
      <span class="sub beta">beta</span>
    </a>
    <div class="spacer"></div>
    <div class="conn">
      <span class="dot" :class="{ offline: !conn.online }"></span>
      {{ conn.online ? 'live' : 'offline' }} · <code>{{ addr }}</code>
    </div>
    <span class="ver">{{ version }}</span>
    <button
      type="button"
      class="theme-btn"
      :title="themeLabel"
      :aria-label="themeLabel"
      @click="theme.cycle()"
    >{{ themeGlyph }}</button>
  </div>
</template>

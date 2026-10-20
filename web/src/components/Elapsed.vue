<script setup lang="ts">
// A time that moves: "12s ago", or how long something has lasted. Its own
// component so that only this span re-renders on the clock's tick, not the row
// or the page around it.
import {ago, now, span} from '../api'

defineProps<{t: number; kind?: 'ago' | 'span'}>()
</script>

<template>
  <span>{{ kind === 'span' ? span(now / 1000 - t) : ago(t, now) }}</span>
</template>

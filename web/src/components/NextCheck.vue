<script setup lang="ts">
// The countdown to the next round, on the same wall-clock grid the prober
// uses. Split out so the page does not re-render every second to move it.
import {computed} from 'vue'
import {now} from '../api'

const props = defineProps<{interval: number}>()

const text = computed(() => {
  const t = now.value / 1000
  const n = Math.max(0, Math.round(Math.ceil(t / props.interval) * props.interval - t))
  return `${Math.floor(n / 60)}:${String(n % 60).padStart(2, '0')}`
})
</script>

<template>
  <span class="num">{{ text }}</span>
</template>

<script setup lang="ts">
// A state is always a word and an icon as well as a colour.
import {computed} from 'vue'
import Icon from './Icon.vue'
import {LABEL, type State} from '../api'

const props = defineProps<{state: State | 'none'}>()
const icon = computed(() => ({operational: 'ok', degraded: 'alert', down: 'fail'})[props.state as string] ?? 'help')
</script>

<template>
  <span
    class="chip"
    :class="`s-${state}`"
    style="color: var(--s); border-color: color-mix(in srgb, var(--s) 35%, transparent); background: color-mix(in srgb, var(--s) 9%, transparent)"
  >
    <Icon :name="icon as any" :size="12" :stroke="2.4" />
    {{ LABEL[state] }}
  </span>
</template>

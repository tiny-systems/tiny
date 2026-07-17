<template>
  <FlowWorkspace
    v-if="project && flowName"
    :key="flowName"
    :client="client"
    :project-name="project"
    :flow-name="flowName"
  />
  <div v-else class="empty">Opening flow…</div>
</template>

<script setup lang="ts">
import { computed, inject } from 'vue'
import { FlowWorkspace } from '@tinysystems/editor'
import type { EditorClient } from '@tinysystems/editor'
import { project } from '../session'

// vue-router passes :id as a prop (props: true on the route).
const props = defineProps<{ id: string }>()

const client = inject<EditorClient>('editorClient')!
const flowName = computed(() => props.id)
</script>

<style scoped>
.empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: #6b7280;
}
</style>

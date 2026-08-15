<template>
  <AgentPage
    v-if="name"
    :key="name"
    :client="client"
    :project-name="name"
  />
  <div v-else class="empty">
    <template v-if="error">{{ error }}</template>
    <template v-else>Connecting to the local cluster…</template>
  </div>
</template>

<script setup lang="ts">
import { computed, inject } from 'vue'
import { AgentPage } from '@tinysystems/editor'
import type { EditorClient } from '@tinysystems/editor'
import { project, sessionError as error } from '../session'

// vue-router passes :project as a prop (props: true on the route). The param
// is optional — /app with no project falls back to the session's project, so
// the finish-setup link tiny prints works even if the name is omitted.
const props = defineProps<{ project?: string }>()

const client = inject<EditorClient>('editorClient')!
const name = computed(() => props.project || project.value)
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

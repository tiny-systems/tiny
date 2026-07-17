<template>
  <ProjectWorkspace
    v-if="project"
    :client="client"
    :project-name="project"
    @open-flow="openFlow"
  />
  <div v-else class="empty">
    <template v-if="error">{{ error }}</template>
    <template v-else>Connecting to the local cluster…</template>
  </div>
</template>

<script setup lang="ts">
import { inject } from 'vue'
import { useRouter } from 'vue-router'
import { ProjectWorkspace } from '@tinysystems/editor'
import type { EditorClient } from '@tinysystems/editor'
import { project, sessionError as error } from '../session'

const client = inject<EditorClient>('editorClient')!
const router = useRouter()

// The project shell emits the flow's resource name; route into the editor.
function openFlow(flowName: string) {
  router.push({ name: 'flow', params: { id: flowName } })
}
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

<template>
  <!-- Full-bleed editor. The ControlPanel inside FlowWorkspace already carries
       the project / flow / scenario controls, so the host adds no chrome of its
       own — the whole viewport is the editor. -->
  <FlowWorkspace
    v-if="project && selectedFlow"
    :key="project + '::' + selectedFlow"
    :client="client"
    :project-name="project"
    :flow-name="selectedFlow"
  />
  <div v-else class="empty">
    <div class="empty-inner">
      <template v-if="loading">Connecting to the local cluster…</template>
      <template v-else-if="error">{{ error }}</template>
      <template v-else-if="!flows.length">
        No flows in <strong>{{ project }}</strong> yet.<br />
        Ask the agent to create one, then reload.
      </template>
      <template v-else>Opening flow…</template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { FlowWorkspace } from '@tinysystems/editor'
import type { EditorClient } from '@tinysystems/editor'

const props = defineProps<{ client: EditorClient }>()

const project = ref('')
const flows = ref<string[]>([])
const selectedFlow = ref('')
const loading = ref(true)
const error = ref('')

onMounted(async () => {
  try {
    // The active project is a tiny-session concept (one project per session),
    // surfaced by the server's /api/session. The first flow opens by default;
    // the ControlPanel's switcher handles moving between flows.
    const session = await fetch('/api/session').then((r) => r.json())
    project.value = session.project || ''
    if (!project.value) {
      error.value = 'No active project in this session.'
      return
    }

    const res: any = await props.client.flow.getFlowList({ ProjectName: project.value })
    flows.value = (res.Flows || [])
      .map((it: any) => it.Flow?.ResourceName)
      .filter(Boolean)
    selectedFlow.value = flows.value[0] || ''
  } catch (e: any) {
    error.value = e?.message || 'Failed to reach the local FlowService.'
  } finally {
    loading.value = false
  }
})
</script>

<style scoped>
.empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: #6b7280;
  text-align: center;
}
.empty-inner {
  max-width: 360px;
  line-height: 1.6;
}
</style>

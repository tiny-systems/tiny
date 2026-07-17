<template>
  <div class="app">
    <header class="topbar">
      <div class="brand">tiny<span class="brand-sub">editor</span></div>

      <div class="ctx">
        <span class="k">project</span>
        <span class="v">{{ project || '—' }}</span>
      </div>

      <div class="ctx" v-if="flows.length">
        <span class="k">flow</span>
        <select v-model="selectedFlow" class="flow-select">
          <option v-for="f in flows" :key="f" :value="f">{{ shortFlow(f) }}</option>
        </select>
      </div>

      <div class="spacer"></div>
      <a class="hint" href="https://github.com/tiny-systems/tiny" target="_blank" rel="noreferrer">local · gRPC-web</a>
    </header>

    <main class="stage">
      <FlowEditor
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
          <template v-else>Select a flow to open.</template>
        </div>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { FlowEditor } from '@tinysystems/editor'
import type { EditorClient } from '@tinysystems/editor'

const props = defineProps<{ client: EditorClient }>()

const project = ref('')
const flows = ref<string[]>([])
const selectedFlow = ref('')
const loading = ref(true)
const error = ref('')

// Resource names look like `.../flows/<id>`; show the readable tail.
function shortFlow(name: string): string {
  const parts = name.split('/')
  return parts[parts.length - 1] || name
}

onMounted(async () => {
  try {
    // The active project is a tiny-session concept (one project per session),
    // surfaced by the server's /api/session. Flows come from the gRPC client
    // itself — the same FlowService the editor streams from.
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

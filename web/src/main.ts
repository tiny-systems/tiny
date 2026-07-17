import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createGrpcWebTransport } from '@connectrpc/connect-web'
import { createClient } from '@connectrpc/connect'
import type { EditorClient } from '@tinysystems/editor'
import { setNavigator } from '@tinysystems/editor'
import '@tinysystems/editor/style.css'

import { FlowService } from './grpc/flow.service_connect'
import { RunsService } from './grpc/runs.service_connect'
import { StatisticsService } from './grpc/statistics.service_connect'
import { ProjectService } from './grpc/project.service_connect'
import App from './App.vue'
import { router } from './router'
import { loadSession } from './session'
import './style.css'

// The tiny CLI serves this SPA and the gRPC-web FlowService off the same
// localhost origin, so the transport targets window.location.origin. No auth
// headers: it's a local single-user cluster. This is the whole "different
// EditorClient" half of one-editor-two-hosts — same components as the
// platform, a client pointed at the machine in front of you.
const transport = createGrpcWebTransport({
  baseUrl: window.location.origin,
})

const client: EditorClient = {
  flow: createClient(FlowService, transport),
  runs: createClient(RunsService, transport),
  statistics: createClient(StatisticsService, transport),
  project: createClient(ProjectService, transport),
}

loadSession()

// Route the editor's platform-style navigation (back-to-project, flow switch)
// through our router: a `.../flow-<id>` path opens that flow; anything else
// (project-level, e.g. the back button) returns to the dashboard.
setNavigator((to: string) => {
  const m = to.match(/\/flow-([^/?#]+)/)
  if (m) {
    router.push({ name: 'flow', params: { id: m[1] } })
  } else {
    router.push({ name: 'project' })
  }
})

const app = createApp(App)
app.provide('editorClient', client)
app.use(createPinia())
app.use(router)
app.mount('#app')

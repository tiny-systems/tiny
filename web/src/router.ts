import { createRouter, createWebHistory } from 'vue-router'
import ProjectView from './views/ProjectView.vue'
import EditorView from './views/EditorView.vue'
import AppView from './views/AppView.vue'

// Three routes mirror the split between building and using:
//   /                → the project dashboard (activity, widgets, flows, …)
//   /flow/:id        → the flow editor for one flow (layer)
//   /app/:project?   → the Agent page — the product surface for USING an
//                      agent (setup + chat + widgets); the link tiny prints
//                      after a build. Project defaults to the session's.
// Client-side routing is what makes the flow switcher, previews and the
// "open flow" navigation work — they push routes instead of hitting dead URLs.
export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'project', component: ProjectView },
    { path: '/flow/:id', name: 'flow', component: EditorView, props: true },
    { path: '/app/:project?', name: 'app', component: AppView, props: true },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

// The tab title names the surface you are on — several are usually open at
// once, and "flow editor" on all of them makes them indistinguishable.
router.afterEach((to) => {
  const title =
    to.name === 'flow' ? `${to.params.id} · flow editor` :
    to.name === 'app' ? `${to.params.project || 'agent'} · tiny` :
    'tiny'
  document.title = title
})

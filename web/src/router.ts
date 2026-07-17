import { createRouter, createWebHistory } from 'vue-router'
import ProjectView from './views/ProjectView.vue'
import EditorView from './views/EditorView.vue'

// Two routes mirror the platform's project shell:
//   /            → the project dashboard (activity, widgets, flows, …)
//   /flow/:id    → the flow editor for one flow (layer)
// Client-side routing is what makes the flow switcher, previews and the
// "open flow" navigation work — they push routes instead of hitting dead URLs.
export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'project', component: ProjectView },
    { path: '/flow/:id', name: 'flow', component: EditorView, props: true },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

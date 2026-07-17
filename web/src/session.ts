import { ref } from 'vue'

// The active project for this tiny session (one project per session), plus any
// error reaching the local backend. Loaded once at startup from /api/session
// and shared reactively across the views.
export const project = ref('')
export const sessionError = ref('')
export const sessionReady = ref(false)

export async function loadSession() {
  try {
    const s = await fetch('/api/session').then((r) => r.json())
    project.value = s.project || ''
    if (!project.value) {
      sessionError.value = 'No active project in this session.'
    }
  } catch (e: any) {
    sessionError.value = e?.message || 'Failed to reach the local FlowService.'
  } finally {
    sessionReady.value = true
  }
}

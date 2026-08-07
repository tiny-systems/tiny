<template>
  <router-view v-if="sessionReady" />
  <div v-else class="boot">Connecting to the local cluster…</div>
  <Notifications />
</template>

<script setup lang="ts">
import { sessionReady } from './session'
import Notifications from './Notifications.vue'
</script>

<style scoped>
.boot {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: #6b7280;
}
</style>

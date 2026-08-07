<!--
  Notification outlet for the editor's notify() calls.

  The editor package raises every user-facing outcome through notiwind —
  failed deploys, signal errors, and now signal acknowledgements — but the
  outlet has to be mounted by the host. tiny never mounted one, so all of it
  was dropped on the floor: a widget submit that failed and a widget submit
  that worked looked exactly the same (nothing happened).

  Three groups, matching what the editor emits: error, success, generic.
-->
<template>
  <NotificationGroup group="error">
    <div class="notify-stack" aria-live="assertive">
      <Notification
        v-slot="{ notifications, close }"
        enter="transform ease-out duration-200 transition"
        enter-from="translate-y-2 opacity-0"
        enter-to="translate-y-0 opacity-100"
        leave="transition ease-in duration-150"
        leave-from="opacity-100"
        leave-to="opacity-0"
        move="transition duration-300"
      >
        <article v-for="n in notifications" :key="n.id" class="notify notify--error">
          <div class="notify__body">
            <p class="notify__title">{{ n.title }}</p>
            <p v-if="n.text" class="notify__text">{{ n.text }}</p>
          </div>
          <button type="button" class="notify__close" aria-label="Dismiss" @click="close(n.id)">×</button>
        </article>
      </Notification>
    </div>
  </NotificationGroup>

  <NotificationGroup group="success">
    <div class="notify-stack" aria-live="polite">
      <Notification
        v-slot="{ notifications, close }"
        enter="transform ease-out duration-200 transition"
        enter-from="translate-y-2 opacity-0"
        enter-to="translate-y-0 opacity-100"
        leave="transition ease-in duration-150"
        leave-from="opacity-100"
        leave-to="opacity-0"
        move="transition duration-300"
      >
        <article v-for="n in notifications" :key="n.id" class="notify notify--success">
          <div class="notify__body">
            <p class="notify__title">{{ n.title }}</p>
            <p v-if="n.text" class="notify__text">{{ n.text }}</p>
          </div>
          <button type="button" class="notify__close" aria-label="Dismiss" @click="close(n.id)">×</button>
        </article>
      </Notification>
    </div>
  </NotificationGroup>

  <NotificationGroup group="generic">
    <div class="notify-stack" aria-live="polite">
      <Notification
        v-slot="{ notifications, close }"
        enter="transform ease-out duration-200 transition"
        enter-from="translate-y-2 opacity-0"
        enter-to="translate-y-0 opacity-100"
        leave="transition ease-in duration-150"
        leave-from="opacity-100"
        leave-to="opacity-0"
        move="transition duration-300"
      >
        <article v-for="n in notifications" :key="n.id" class="notify">
          <div class="notify__body">
            <p class="notify__title">{{ n.title }}</p>
            <p v-if="n.text" class="notify__text">{{ n.text }}</p>
          </div>
          <button type="button" class="notify__close" aria-label="Dismiss" @click="close(n.id)">×</button>
        </article>
      </Notification>
    </div>
  </NotificationGroup>
</template>

<script setup lang="ts">
import { Notification, NotificationGroup } from 'notiwind'
</script>

<style scoped>
.notify-stack {
  position: fixed;
  right: 1rem;
  bottom: 1rem;
  z-index: 60;
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 0.5rem;
  pointer-events: none;
}

.notify {
  pointer-events: auto;
  display: flex;
  align-items: flex-start;
  gap: 0.75rem;
  max-width: min(26rem, calc(100vw - 2rem));
  padding: 0.75rem 0.875rem;
  border-radius: 0.5rem;
  border: 1px solid #e5e7eb;
  border-left-width: 3px;
  background: #fff;
  box-shadow: 0 8px 24px rgb(0 0 0 / 12%);
}

.notify--error {
  border-left-color: #dc2626;
}

.notify--success {
  border-left-color: #059669;
}

.notify__title {
  font-size: 0.875rem;
  font-weight: 600;
  color: #111827;
}

.notify__text {
  margin-top: 0.125rem;
  font-size: 0.8125rem;
  line-height: 1.35;
  color: #4b5563;
  overflow-wrap: anywhere;
}

.notify__close {
  margin-left: auto;
  font-size: 1.125rem;
  line-height: 1;
  color: #9ca3af;
  cursor: pointer;
}

.notify__close:hover {
  color: #4b5563;
}

@media (prefers-color-scheme: dark) {
  .notify {
    border-color: #1f2937;
    background: #0b0b10;
    box-shadow: 0 8px 24px rgb(0 0 0 / 50%);
  }

  .notify__title {
    color: #f3f4f6;
  }

  .notify__text {
    color: #9ca3af;
  }
}
</style>

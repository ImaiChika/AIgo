<script setup>
import { currentUser } from "../auth.js";
</script>

<template>
  <!-- Keep both editing modes alive while moving within the generation area.
       A different account, a different working identity, or leaving this area
       destroys the cached forms — the key must change with the identity, or a
       cached page keeps polling/submitting under the previous role's token. -->
  <RouterView v-slot="{ Component }">
    <KeepAlive :key="[currentUser?.id || currentUser?.username, currentUser?.role || ''].join(':')" :max="2">
      <component :is="Component" />
    </KeepAlive>
  </RouterView>
</template>

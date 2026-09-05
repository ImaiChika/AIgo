<script setup>
import { currentUser } from "../auth.js";
</script>

<template>
  <!-- Keep both editing modes alive while moving within the generation area.
       A different account or leaving this area destroys the cached forms. -->
  <RouterView v-slot="{ Component }">
    <KeepAlive :key="currentUser?.id || currentUser?.username" :max="2">
      <component :is="Component" />
    </KeepAlive>
  </RouterView>
</template>
